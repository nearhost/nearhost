package config

import (
	"os"
	"path/filepath"
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

func TestValidateRequiresExactlyOneWebRoute(t *testing.T) {
	c := sample()
	c.Projects[0].Routes[0].Role = RoleIgnore
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("expected missing web error, got %v", err)
	}
	c.Projects[0].Routes[0].Role = RoleWeb
	c.Projects[0].Routes[1].Role = RoleWeb
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("expected multiple web error, got %v", err)
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
