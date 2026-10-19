package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func sample() *Config {
	return &Config{
		Servers: []Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []Project{{
			Name: "myapp", Server: "mini", Path: "/srv/myapp", Alias: "myapp.test", Enabled: true,
			Routes: []Route{
				{Port: 80, Role: RoleWeb, Label: "Laravel (APP_PORT)"},
				{Port: 5174, Role: RoleLoopback, Label: "Vite (VITE_PORT)"},
				{Port: 3306, Role: RoleIgnore, Label: "MySQL (FORWARD_DB_PORT)"},
			},
		}},
	}
}

func TestLoadMissingFileReturnsEmptyConfig(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope", "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Servers) != 0 || len(c.Projects) != 0 {
		t.Fatalf("expected empty config, got %+v", c)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "dir", "config.yaml")
	if err := Save(path, sample()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0].Alias != "myapp.test" || len(got.Projects[0].Routes) != 3 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Projects[0].Routes[1].Role != RoleLoopback {
		t.Fatalf("expected loopback role, got %q", got.Projects[0].Routes[1].Role)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "ssh_host: mini") {
		t.Fatalf("expected snake_case keys in yaml, got:\n%s", raw)
	}
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("projects: [unclosed"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}

func TestValidateAcceptsSample(t *testing.T) {
	if err := sample().Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateRejectsDuplicateAlias(t *testing.T) {
	c := sample()
	p := c.Projects[0]
	p.Name = "other"
	c.Projects = append(c.Projects, p)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("expected alias error, got %v", err)
	}
}

func TestValidateRejectsDuplicateLoopbackPortAcrossEnabledProjects(t *testing.T) {
	c := sample()
	p := c.Projects[0]
	p.Name, p.Alias = "other", "other.test"
	c.Projects = append(c.Projects, p)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "5174") {
		t.Fatalf("expected loopback clash error, got %v", err)
	}
	c.Projects[1].Enabled = false
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled project should not clash: %v", err)
	}
}

func TestValidateRequiresAWebRoute(t *testing.T) {
	c := sample()
	c.Projects[0].Routes[0].Role = RoleIgnore
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "at least one web route") {
		t.Fatalf("expected missing web error, got %v", err)
	}
	c.Projects[0].Routes[0].Role = RoleWeb
	c.Projects[0].Routes[1].Role = RoleWeb
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "needs at least one host") {
		t.Fatalf("expected second web route without hosts error, got %v", err)
	}
}

func TestValidateRejectsUnknownServerAndReservedLocal(t *testing.T) {
	c := sample()
	c.Projects[0].Server = "ghost"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected unknown server error, got %v", err)
	}
	c = sample()
	c.Servers = append(c.Servers, Server{Name: LocalServer, SSHHost: "x"})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved error, got %v", err)
	}
}

func TestValidateRejectsBadRoleAndPort(t *testing.T) {
	c := sample()
	c.Projects[0].Routes[2].Role = "sideways"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("expected role error, got %v", err)
	}
	c = sample()
	c.Projects[0].Routes[2].Port = 70000
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected port error, got %v", err)
	}
}

func TestProjectUsesLocalServerWithoutConfiguringIt(t *testing.T) {
	c := sample()
	c.Projects[0].Server = LocalServer
	if err := c.Validate(); err != nil {
		t.Fatalf("local should be implicit: %v", err)
	}
	s, ok := c.Server(LocalServer)
	if !ok || s.Name != LocalServer || s.SSHHost != "" {
		t.Fatalf("expected implicit local server, got %+v %v", s, ok)
	}
}

func TestAliasFor(t *testing.T) {
	cases := map[string]string{
		"myapp":        "myapp.test",
		"My App_2":     "my-app-2.test",
		"  spaced  ":   "spaced.test",
		"weird!!name":  "weird-name.test",
		"-lead-trail-": "lead-trail.test",
	}
	for in, want := range cases {
		if got := AliasFor(in); got != want {
			t.Errorf("AliasFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateRejectsNamesThatNormaliseToSameAlias(t *testing.T) {
	c := sample()
	p := c.Projects[0]
	p.Name, p.Alias = "My-App", AliasFor("My-App")
	c.Projects[0].Alias = AliasFor("my_app")
	c.Projects = append(c.Projects, p)
	if err := c.Validate(); err == nil {
		t.Fatal("expected duplicate alias error")
	}
}

func TestUpsertAndRemoveProject(t *testing.T) {
	c := sample()
	c.UpsertProject(Project{Name: "myapp", Server: "mini", Path: "/new", Alias: "myapp.test",
		Routes: []Route{{Port: 80, Role: RoleWeb}}})
	if p, _ := c.Project("myapp"); p.Path != "/new" || len(c.Projects) != 1 {
		t.Fatalf("upsert should replace in place: %+v", c.Projects)
	}
	c.UpsertProject(Project{Name: "second", Server: "mini", Alias: "second.test",
		Routes: []Route{{Port: 80, Role: RoleWeb}}})
	if len(c.Projects) != 2 {
		t.Fatal("upsert should append new project")
	}
	if !c.RemoveProject("second") || c.RemoveProject("second") {
		t.Fatal("remove should report whether it removed")
	}
}

func TestWebRoute(t *testing.T) {
	p := sample().Projects[0]
	r, ok := p.WebRoute()
	if !ok || r.Port != 80 {
		t.Fatalf("expected web route 80, got %+v %v", r, ok)
	}
	p.Routes = nil
	if _, ok := p.WebRoute(); ok {
		t.Fatal("expected no web route")
	}
}

func TestPathsHonourXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	d, err := Dir()
	if err != nil || d != "/tmp/xdg/nearhost" {
		t.Fatalf("Dir = %q, %v", d, err)
	}
	p, _ := DefaultPath()
	if p != "/tmp/xdg/nearhost/config.yaml" {
		t.Fatalf("DefaultPath = %q", p)
	}
	r, _ := RunDir()
	cd, _ := CaddyDir()
	if r != "/tmp/xdg/nearhost/run" || cd != "/tmp/xdg/nearhost/caddy" {
		t.Fatalf("RunDir/CaddyDir = %q %q", r, cd)
	}
}

func TestPathsDefaultToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	d, err := Dir()
	if err != nil || d != filepath.Join(home, ".config", "nearhost") {
		t.Fatalf("Dir = %q, %v", d, err)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "config.yaml" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestPathsIgnoreRelativeXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/xdg")
	home, _ := os.UserHomeDir()
	d, err := Dir()
	if err != nil || d != filepath.Join(home, ".config", "nearhost") {
		t.Fatalf("Dir = %q, %v", d, err)
	}
}

func TestAliasForKeepsLabelWithinDNSLimit(t *testing.T) {
	long := strings.Repeat("a", 70)
	a, b := AliasFor(long+"-one"), AliasFor(long+"-two")
	for _, alias := range []string{a, b} {
		label := strings.TrimSuffix(alias, ".test")
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			t.Fatalf("label %q (%d chars) is not a valid DNS label", label, len(label))
		}
	}
	if a == b {
		t.Fatalf("distinct long names share alias %q", a)
	}
	if AliasFor(long+"-one") != a {
		t.Fatal("alias should be stable for the same name")
	}
	exact := strings.Repeat("b", 63)
	if got := AliasFor(exact); got != exact+".test" {
		t.Fatalf("a 63 character name should be kept whole, got %q", got)
	}
}

func TestValidateRejectsAliasesDifferingOnlyByCase(t *testing.T) {
	c := sample()
	p := c.Projects[0]
	p.Name, p.Alias, p.Enabled = "other", "MyApp.test", false
	c.Projects = append(c.Projects, p)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("expected alias error, got %v", err)
	}
}

func TestPresetsRoundTripAndStayOutOfAnEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	c := sample()
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "presets") || strings.Contains(string(data), "registry") {
		t.Fatalf("a config without presets should not mention them:\n%s", data)
	}

	c.UpsertPreset(PresetRef{Name: "acme/stack", Ref: "3f2c1ab9", SHA256: "9a41"})
	c.UpsertPreset(PresetRef{Name: "mine"})
	c.UpsertPreset(PresetRef{Name: "acme/stack", Ref: "aaaaaaa", SHA256: "bbbb"})
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Presets) != 2 {
		t.Fatalf("loaded %+v", got)
	}
	if p, ok := got.Preset("acme/stack"); !ok || p.Ref != "aaaaaaa" || p.SHA256 != "bbbb" {
		t.Errorf("stack = %+v %v", p, ok)
	}
	if p, ok := got.Preset("mine"); !ok || p.Ref != "" {
		t.Errorf("mine = %+v %v", p, ok)
	}
	if !got.RemovePreset("mine") || got.RemovePreset("mine") || len(got.Presets) != 1 {
		t.Errorf("RemovePreset left %+v", got.Presets)
	}
}

func TestValidateRejectsBadPresets(t *testing.T) {
	c := sample()
	c.Presets = []PresetRef{{Name: "stack"}, {Name: "stack"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `duplicate preset "stack"`) {
		t.Errorf("expected a duplicate error, got %v", err)
	}
	for _, name := range []string{"", "../etc", "My Preset", "a/b/c", "/stack", "acme/", "acme/../etc", "Acme/stack"} {
		c.Presets = []PresetRef{{Name: name}}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "preset name") {
			t.Errorf("name %q: expected a name error, got %v", name, err)
		}
	}
}

// trusting is the sample config with two registries.
func trusting() *Config {
	c := sample()
	c.Registries = []Registry{{Alias: "acme", Repo: "acme/presets"}, {Alias: "team-web", Repo: "Team/web.presets"}}
	return c
}

func TestValidateAcceptsRegistriesAndTheirPresets(t *testing.T) {
	c := trusting()
	c.Presets = []PresetRef{
		{Name: "acme/stack", Ref: "3f2c1ab", SHA256: "9a41"},
		{Name: "team-web/stack", Ref: "3f2c1ab", SHA256: "9a41"},
		{Name: "stack"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if r, ok := c.Trusted("team-web"); !ok || r.Repo != "Team/web.presets" {
		t.Errorf("Trusted(team-web) = %+v %v", r, ok)
	}
	if _, ok := c.Trusted("nope"); ok {
		t.Error("Trusted found a registry that is not listed")
	}
}

func TestValidateRejectsBadRegistries(t *testing.T) {
	cases := map[string]struct {
		registries []Registry
		want       string
	}{
		"alias in capitals":   {[]Registry{{Alias: "Acme", Repo: "acme/presets"}}, `registry alias "Acme"`},
		"empty alias":         {[]Registry{{Alias: "", Repo: "acme/presets"}}, `registry alias ""`},
		"alias with a slash":  {[]Registry{{Alias: "a/b", Repo: "acme/presets"}}, `registry alias "a/b"`},
		"repo without owner":  {[]Registry{{Alias: "acme", Repo: "presets"}}, `repo "presets"`},
		"repo with a path":    {[]Registry{{Alias: "acme", Repo: "acme/presets/main"}}, `repo "acme/presets/main"`},
		"repo of dots":        {[]Registry{{Alias: "acme", Repo: "acme/.."}}, `repo "acme/.."`},
		"owner with a dot":    {[]Registry{{Alias: "acme", Repo: "ac.me/presets"}}, `repo "ac.me/presets"`},
		"owner with a hyphen": {[]Registry{{Alias: "acme", Repo: "-acme/presets"}}, `repo "-acme/presets"`},
		"owner too long":      {[]Registry{{Alias: "acme", Repo: strings.Repeat("a", 40) + "/presets"}}, "is not a GitHub repository"},
		"name too long":       {[]Registry{{Alias: "acme", Repo: "acme/" + strings.Repeat("a", 101)}}, "is not a GitHub repository"},
		"alias twice":         {[]Registry{{Alias: "acme", Repo: "acme/one"}, {Alias: "acme", Repo: "acme/two"}}, `duplicate registry alias "acme"`},
		"repo twice":          {[]Registry{{Alias: "one", Repo: "acme/presets"}, {Alias: "two", Repo: "Acme/Presets"}}, `trusted twice, as "one" and "two"`},
	}
	for name, tc := range cases {
		c := sample()
		c.Registries = tc.registries
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected an error with %q, got %v", name, tc.want, err)
		}
	}
	c := sample()
	c.Registries = []Registry{{Alias: "acme", Repo: strings.Repeat("a", 39) + "/" + strings.Repeat("b", 100)}}
	if err := c.Validate(); err != nil {
		t.Errorf("the longest owner and name GitHub allows should pass: %v", err)
	}
}

// A config edited by hand can name a preset whose registry is gone. The
// error names both and says how to recover.
func TestValidateRejectsAPresetFromARegistryThatIsNotTrusted(t *testing.T) {
	c := trusting()
	c.Presets = []PresetRef{{Name: "gone/stack", Ref: "3f2c1ab", SHA256: "9a41"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate accepted a preset from an untrusted registry")
	}
	for _, want := range []string{`"gone/stack"`, `registry "gone"`, "add the registry back under registries or remove the preset from presets"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestValidateRequiresARefAndHashForRegistryPresets(t *testing.T) {
	for _, p := range []PresetRef{{Name: "acme/stack"}, {Name: "acme/stack", Ref: "3f2c1ab"}, {Name: "acme/stack", SHA256: "9a41"}} {
		c := trusting()
		c.Presets = []PresetRef{p}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "needs a ref and a sha256") {
			t.Errorf("%+v: expected a ref error, got %v", p, err)
		}
	}
}

func TestSplitPreset(t *testing.T) {
	for name, want := range map[string][2]string{
		"stack":      {"", "stack"},
		"acme/stack": {"acme", "stack"},
	} {
		if alias, plain := SplitPreset(name); alias != want[0] || plain != want[1] {
			t.Errorf("SplitPreset(%q) = %q, %q", name, alias, plain)
		}
	}
}

func TestValidAlias(t *testing.T) {
	for _, s := range []string{"acme", "team-web", "a1"} {
		if !ValidAlias(s) {
			t.Errorf("ValidAlias(%q) = false", s)
		}
	}
	for _, s := range []string{"", "Acme", "team--web", "-acme", "acme-", "a_b", "a/b", "a.b"} {
		if ValidAlias(s) {
			t.Errorf("ValidAlias(%q) = true", s)
		}
	}
}

func TestRegistriesRoundTripAndStayOutOfAnEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "registries") {
		t.Fatalf("a config without registries should not mention them:\n%s", data)
	}
	if err := Save(path, trusting()); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Registries) != 2 || got.Registries[1] != (Registry{Alias: "team-web", Repo: "Team/web.presets"}) {
		t.Errorf("registries = %+v", got.Registries)
	}
}

func TestValidateRejectsARefOnALocalPreset(t *testing.T) {
	for _, p := range []PresetRef{{Name: "stack", Ref: "3f2c1ab"}, {Name: "stack", SHA256: "9a41"}} {
		c := trusting()
		c.Presets = []PresetRef{p}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "is a local file") {
			t.Errorf("%+v: expected a local file error, got %v", p, err)
		}
	}
}

func TestRegistryPromptIsOffOnlyWhenFalse(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]bool{"": false, "registry_prompt: true\n": false, "registry_prompt: false\n": true} {
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load %q: %v", body, err)
		}
		if c.RegistryPromptOff() != want {
			t.Errorf("%q: RegistryPromptOff = %v, want %v", body, !want, want)
		}
	}
}

func TestRegistryPromptRoundTripsAndStaysOutOfAnEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "registry_prompt") {
		t.Errorf("an unset registry_prompt was written:\n%s", raw)
	}
	c := sample()
	off := false
	c.RegistryPrompt = &off
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "registry_prompt: false\n") {
		t.Errorf("registry_prompt was not written:\n%s", raw)
	}
	got, err := Load(path)
	if err != nil || !got.RegistryPromptOff() {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestNotificationsAreOnUnlessTurnedOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for content, off := range map[string]bool{"": false, "notifications: true\n": false, "notifications: false\n": true} {
		os.WriteFile(path, []byte(content), 0o600)
		cfg, err := Load(path)
		if err != nil || cfg.NotificationsOff() != off {
			t.Errorf("%q: off %v, err %v", content, cfg.NotificationsOff(), err)
		}
	}
}

func TestValidateWebRoutesAndHosts(t *testing.T) {
	base := func(routes ...Route) *Config {
		return &Config{Projects: []Project{{Name: "dd", Server: LocalServer, Alias: "dd.test", Enabled: true, Routes: routes}}}
	}
	web := func(port int, hosts ...string) Route { return Route{Port: port, Role: RoleWeb, Hosts: hosts} }
	cases := []struct {
		name    string
		cfg     *Config
		wantErr string // "" means valid
	}{
		{"legacy", base(web(8220)), ""},
		{"several web routes", base(web(8220, "app.local.dd.io", "*.status.local.dd.io"), web(8230, "ws.local.dd.io")), ""},
		{"no web route", base(Route{Port: 1, Role: RoleLoopback}), "at least one web route"},
		{"second web route without hosts", base(web(8220), web(8230)), "port 8230 needs at least one host"},
		{"bad host", base(web(8220, "*.com")), `"*.com"`},
		{"host twice in project", base(web(8220, "a.dd.io"), web(8230, "a.dd.io")), `"a.dd.io"`},
		{"host equals alias", base(web(8220), web(8230, "dd.test")), `"dd.test"`},
		{"alias on the primary route is tolerated", base(web(8220, "dd.test")), ""},
		{"hosts on a loopback route", base(web(8220), Route{Port: 5432, Role: RoleLoopback, Hosts: []string{"db.dd.io"}}), "only web routes have hosts"},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: Validate() = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func TestProjectWebPutsAliasOnPrimary(t *testing.T) {
	p := Project{Alias: "dd.test", Routes: []Route{
		{Port: 5432, Role: RoleLoopback},
		{Port: 8220, Role: RoleWeb, Hosts: []string{"app.local.dd.io", "dd.test"}},
		{Port: 8230, Role: RoleWeb, Label: "reverb", Hosts: []string{"ws.local.dd.io"}},
	}}
	want := []WebHosts{
		{Port: 8220, Hosts: []string{"dd.test", "app.local.dd.io"}},
		{Port: 8230, Label: "reverb", Hosts: []string{"ws.local.dd.io"}},
	}
	if got := p.Web(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Web() = %+v, want %+v", got, want)
	}
}

func TestHostClashIgnoresDisabledAndMatchesPatternsExactly(t *testing.T) {
	pro := Project{Name: "pro", Alias: "pro.test", Enabled: true, Routes: []Route{{Port: 8211, Role: RoleWeb, Hosts: []string{"*.local.redactr.pro"}}}}
	old := Project{Name: "old", Alias: "old.test", Enabled: false, Routes: []Route{{Port: 1, Role: RoleWeb, Hosts: []string{"auth.local.redactr.pro"}}}}
	c := &Config{Projects: []Project{pro, old}}
	platform := Project{Name: "platform", Alias: "platform.test", Routes: []Route{{Port: 8210, Role: RoleWeb, Hosts: []string{"auth.local.redactr.pro"}}}}
	if _, _, clash := c.HostClash(platform); clash {
		t.Error("an exact host under another project's pattern is not a clash, and disabled projects don't count")
	}
	copyPro := Project{Name: "pro2", Alias: "pro2.test", Routes: []Route{{Port: 9, Role: RoleWeb, Hosts: []string{"*.LOCAL.redactr.pro"}}}}
	if host, other, clash := c.HostClash(copyPro); !clash || other != "pro" || host != "*.LOCAL.redactr.pro" {
		t.Errorf("HostClash = %q %q %v, want the pattern, pro, true", host, other, clash)
	}
	if _, _, clash := c.HostClash(pro); clash {
		t.Error("a project does not clash with itself")
	}
}

func TestValidProjectAliasAcceptsAnyExactDomain(t *testing.T) {
	for _, ok := range []string{"app.test", "app.local.domaindash.io", "a-b.example.com"} {
		if err := ValidProjectAlias(ok); err != nil {
			t.Errorf("ValidProjectAlias(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"*.x.io", "localhost", "127.0.0.1", "::1", "App.Test", "", ".test", "*.test", "a_b.test"} {
		want := fmt.Sprintf("invalid main name %q: must be a lowercase domain name, not a wildcard, localhost or an IP address", bad)
		if err := ValidProjectAlias(bad); err == nil || err.Error() != want {
			t.Errorf("ValidProjectAlias(%q) = %v, want %q", bad, err, want)
		}
	}
}

func TestHostsFileNamesSkipsRealDomainAliasOfDisabledProject(t *testing.T) {
	c := &Config{Projects: []Project{{Name: "a", Alias: "app.local.x.io"}, {Name: "b", Alias: "b.test"}}}
	if got := c.HostsFileNames(); !slices.Equal(got, []string{"b.test"}) {
		t.Fatalf("disabled: HostsFileNames() = %v", got)
	}
	c.Projects[0].Enabled = true
	if got := c.HostsFileNames(); !slices.Equal(got, []string{"app.local.x.io", "b.test"}) {
		t.Fatalf("enabled: HostsFileNames() = %v", got)
	}
}

func TestHostsFileNamesDropsOldAliasOnRename(t *testing.T) {
	c := &Config{Projects: []Project{{Name: "dd", Alias: "dd.test", Enabled: true}}}
	c.Projects[0].Alias = "app.local.x.io"
	if got := c.HostsFileNames(); !slices.Equal(got, []string{"app.local.x.io"}) {
		t.Fatalf("HostsFileNames() = %v", got)
	}
}

func TestHostsFileNamesDedupAndSort(t *testing.T) {
	c := &Config{Projects: []Project{{Name: "b", Alias: "b.test"}, {Name: "a", Alias: "a.test"}, {Name: "c", Alias: "b.test"}}}
	if got := c.HostsFileNames(); !slices.Equal(got, []string{"a.test", "b.test"}) {
		t.Fatalf("HostsFileNames() = %v", got)
	}
}

// The hosts file names are the valid aliases and the exact hosts of every web
// route of an enabled project: never localhost, a pattern, an invalid alias,
// a host of a route that is not a web route or a disabled project's host.
func TestHostsFileNamesHoldExactHostsOfWebRoutes(t *testing.T) {
	c := &Config{Projects: []Project{
		{Name: "l1", Alias: "l1.test", Enabled: true, Routes: []Route{{Port: 8242, Role: RoleWeb, Hosts: []string{"localhost"}}}},
		{Name: "pro", Alias: "pro.test", Enabled: true, Routes: []Route{
			{Port: 8211, Role: RoleWeb, Hosts: []string{"*.local.redactr.pro", "X.local.redactr.pro"}},
			{Port: 8212, Role: RoleWeb, Hosts: []string{"ws.local.redactr.pro", "pro.test"}},
			{Port: 5432, Role: RoleLoopback, Hosts: []string{"db.local.redactr.pro"}},
		}},
		{Name: "bad", Alias: "Bad.example.io", Enabled: true, Routes: []Route{{Port: 9000, Role: RoleWeb, Hosts: []string{"x.local.redactr.pro"}}}},
		// A disabled project keeps its alias, as before, but its real-domain
		// hosts stay out: they would shadow DNS for a site nothing serves.
		{Name: "off", Alias: "off.test", Routes: []Route{{Port: 80, Role: RoleWeb, Hosts: []string{"staging.example.io"}}}},
	}}
	want := []string{"l1.test", "off.test", "pro.test", "ws.local.redactr.pro", "x.local.redactr.pro"}
	if got := c.HostsFileNames(); !slices.Equal(got, want) {
		t.Fatalf("HostsFileNames() = %v, want %v", got, want)
	}
}

func TestNearhostrcSnapshotValidatesAndRoundTrips(t *testing.T) {
	cfg := sample()
	cfg.Projects[0].Nearhostrc = &RcSnapshot{SHA256: "abc", Routes: []Route{{Port: 8220, Role: RoleWeb, Hosts: []string{"app.local.dd.io", "*.x.dd.io"}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Projects[0].Nearhostrc, cfg.Projects[0].Nearhostrc) {
		t.Fatalf("snapshot = %+v", got.Projects[0].Nearhostrc)
	}
	cfg.Projects[0].Nearhostrc.Routes[0].Hosts = []string{"bad host"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an invalid snapshot host to be rejected")
	}
}

func TestValidateRefusesDockerClientsOnAServer(t *testing.T) {
	c := sample()
	c.Projects[0].Nearhostrc = &RcSnapshot{SHA256: "h", DockerClients: true}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `project "myapp"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRcSnapshotCloneKeepsEverything(t *testing.T) {
	var nilSnap *RcSnapshot
	if nilSnap.Clone() != nil {
		t.Fatal("nil should clone to nil")
	}
	orig := &RcSnapshot{SHA256: "h", DockerClients: true, Alias: "a.test", Routes: []Route{{Port: 80, Role: RoleWeb, Label: "l", Hosts: []string{"a.test"}}}}
	clone := orig.Clone()
	if !reflect.DeepEqual(clone, orig) {
		t.Fatalf("clone = %+v", clone)
	}
	clone.SHA256 = "x"
	clone.DockerClients = false
	clone.Alias = "b.test"
	clone.Routes[0].Port = 81
	clone.Routes[0].Hosts[0] = "b.test"
	if orig.SHA256 != "h" || !orig.DockerClients || orig.Alias != "a.test" || orig.Routes[0].Port != 80 || orig.Routes[0].Hosts[0] != "a.test" {
		t.Fatalf("original changed: %+v", orig)
	}
}

func TestMovePrimary(t *testing.T) {
	ports := func(rs []Route) []int {
		var out []int
		for _, r := range rs {
			out = append(out, r.Port)
		}
		return out
	}
	in := []Route{{Port: 80, Role: RoleIgnore}, {Port: 6001, Role: RoleWeb}, {Port: 8220, Role: RoleWeb}}
	if got := ports(MovePrimary(in, 8220)); !reflect.DeepEqual(got, []int{80, 8220, 6001}) {
		t.Errorf("8220: %v", got)
	}
	if !reflect.DeepEqual(ports(in), []int{80, 6001, 8220}) {
		t.Errorf("input modified: %v", ports(in))
	}
	if got := ports(MovePrimary(in, 6001)); !reflect.DeepEqual(got, []int{80, 6001, 8220}) {
		t.Errorf("6001: %v", got)
	}
	if got := ports(MovePrimary(in, 9999)); !reflect.DeepEqual(got, []int{80, 6001, 8220}) {
		t.Errorf("absent: %v", got)
	}
}
