package cli

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/tui"
)

func TestSSHHosts(t *testing.T) {
	data := []byte(`# a comment
Host mini mini.lan
  HostName 192.168.1.10
host apollo
Host=vps
Host *.internal !bastion web?
Host "quoted"
Host mini
Match host other
  User dev
`)
	want := []string{"mini", "mini.lan", "apollo", "vps", "quoted"}
	if got := sshHosts(data); !reflect.DeepEqual(got, want) {
		t.Errorf("sshHosts = %v, want %v", got, want)
	}
	u := &uiEngine{b: uiBackend{sshConfig: func() ([]byte, error) { return data, nil }}}
	if got := u.SSHHosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("SSHHosts = %v", got)
	}
	u.b.sshConfig = func() ([]byte, error) { return nil, os.ErrNotExist }
	if got := u.SSHHosts(); got != nil {
		t.Errorf("SSHHosts with no config = %v", got)
	}
}

// formEngine is the interface's backend over a config that trusts the test
// registry, which offers the stack and bundler presets.
func formEngine(t *testing.T) *uiEngine {
	t.Helper()
	cfgPath, _, presets := trusted(t)
	return &uiEngine{b: uiBackend{presets: presets}, path: cfgPath}
}

func TestFormDiscoveryOffersPresetsAndEnablesThem(t *testing.T) {
	u := formEngine(t)
	ctx := context.Background()
	files, err := u.OpenServer(ctx, config.Server{Name: config.LocalServer})
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	dir := fixture(t, "sail-custom")
	d, err := files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != dir || d.Name != filepath.Base(dir) || d.Alias != config.AliasFor(filepath.Base(dir)) || len(d.Routes) == 0 {
		t.Errorf("draft = %+v", d)
	}
	var offered []string
	for _, p := range d.Presets {
		offered = append(offered, p.Name)
		if p.Repo != testReg.Repo || p.Title == "" {
			t.Errorf("preset %+v", p)
		}
	}
	if len(offered) != 2 {
		t.Fatalf("offered %v", offered)
	}

	if notes := u.EnablePresets(ctx, []string{"test/stack", "test/missing"}); len(notes) != 1 || !strings.Contains(notes[0], "test/missing preset was not enabled") {
		t.Errorf("notes = %v", notes)
	}
	if _, ok := loadSaved(t, u.path).Preset("test/stack"); !ok {
		t.Error("test/stack was not enabled")
	}
	d, err = files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Presets) != 1 || d.Presets[0].Name != "test/bundler" {
		t.Errorf("after enabling stack, offered %+v", d.Presets)
	}
	if len(d.Advisories) == 0 {
		t.Error("no advisories for the stack project")
	}
}

func TestFormListsAndResolvesUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "code", "blog"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := formEngine(t)
	files, err := u.OpenServer(context.Background(), config.Server{Name: config.LocalServer})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := files.ListDirs(context.Background(), "~/code"); err != nil || !reflect.DeepEqual(got, []string{"blog"}) {
		t.Errorf("ListDirs = %v, %v", got, err)
	}
	if got, err := files.Resolve(context.Background(), "~/code/blog"); err != nil || got != filepath.Join(home, "code", "blog") {
		t.Errorf("Resolve = %q, %v", got, err)
	}
}

func TestFormRegistryQuestion(t *testing.T) {
	cfgPath, _, b := asking(t, false)
	u := &uiEngine{b: uiBackend{presets: b.presets}, path: cfgPath}
	ctx := context.Background()
	if !u.RegistryQuestion() {
		t.Fatal("no question with no registry trusted")
	}
	if note, err := u.AnswerRegistry(ctx, tui.RegistryNo); note != "" || err != nil || !u.RegistryQuestion() {
		t.Errorf("no: %q %v, asks again %v", note, err, u.RegistryQuestion())
	}
	if note, err := u.AnswerRegistry(ctx, tui.RegistryYes); note != "" || err != nil || u.RegistryQuestion() {
		t.Errorf("yes: %q %v, asks again %v", note, err, u.RegistryQuestion())
	}
	if _, ok := loadSaved(t, cfgPath).Trusted("nearhost"); !ok {
		t.Error("yes did not trust the official registry")
	}

	cfgPath, _, b = asking(t, false)
	u = &uiEngine{b: uiBackend{presets: b.presets}, path: cfgPath}
	if _, err := u.AnswerRegistry(ctx, tui.RegistryNever); err != nil || u.RegistryQuestion() {
		t.Errorf("never: %v, asks again %v", err, u.RegistryQuestion())
	}
}

func TestFormSavesProjectsAndServers(t *testing.T) {
	u := formEngine(t)
	p := config.Project{Name: "blog", Server: config.LocalServer, Path: "/code/blog", Alias: "blog.test", Enabled: true,
		Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}
	if nameErr, aliasErr := u.CheckNames("blog", "blog.test"); nameErr != nil || aliasErr != nil {
		t.Fatalf("CheckNames = %v, %v", nameErr, aliasErr)
	}
	if err := u.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	nameErr, aliasErr := u.CheckNames("blog", "blog.test")
	if nameErr == nil || !strings.Contains(nameErr.Error(), `a project called "blog" already exists`) || aliasErr == nil {
		t.Errorf("CheckNames after saving = %v, %v", nameErr, aliasErr)
	}
	if err := u.SaveProject(p); err == nil {
		t.Error("the same project was saved twice")
	}
	if err := u.AddServer(config.Server{Name: "mini", SSHHost: "mini.lan"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSaved(t, u.path).Server("mini"); !ok {
		t.Error("the server was not saved")
	}
}

func TestFormCheckServer(t *testing.T) {
	u := formEngine(t)
	u.b.up.ssh = sftpOnly(t, t.TempDir())
	if err := u.CheckServer(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}); err != nil {
		t.Errorf("an SFTP-only server failed the check: %v", err)
	}
	u.b.up.ssh, _ = fakeClient(t, "FAKE_SSH_AUTH_FAIL=1")
	err := u.CheckServer(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") || !strings.Contains(err.Error(), "run `ssh mini.lan` once in a terminal to fix access, then press r to check again") {
		t.Errorf("refused check = %v", err)
	}
	_, err = u.OpenServer(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"})
	if err == nil || !strings.Contains(err.Error(), "then press r to connect again") {
		t.Errorf("refused connection = %v", err)
	}
}

// Advice is worked out again for the roles and alias the form ends with.
func TestFormAdvisesForTheFinalChoices(t *testing.T) {
	u := formEngine(t)
	ctx := context.Background()
	if notes := u.EnablePresets(ctx, []string{"test/stack"}); len(notes) != 0 {
		t.Fatal(notes)
	}
	files, err := u.OpenServer(ctx, config.Server{Name: config.LocalServer})
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	dir := fixture(t, "sail-custom")
	d, err := files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	advisories, _, err := files.Advise(ctx, dir, d.Routes, "other.test")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range advisories {
		if a.Code == "app-url" {
			found = strings.Contains(a.Message, "https://other.test")
		}
	}
	if !found {
		t.Errorf("no app-url advisory for other.test: %+v", advisories)
	}
}

// A loopback port another project forwards is refused in the form's words,
// without the hints for `nearhost add`.
func TestFormSaveRefusesALoopbackClashInItsWords(t *testing.T) {
	u := formEngine(t)
	lb := config.Route{Port: 5173, Role: config.RoleLoopback}
	if err := u.AddServer(config.Server{Name: "mini", SSHHost: "mini"}); err != nil {
		t.Fatal(err)
	}
	a := config.Project{Name: "a", Server: "mini", Path: "/a", Alias: "a.test", Enabled: true, Routes: []config.Route{{Port: 80, Role: config.RoleWeb}, lb}}
	if err := u.SaveProject(a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.Name, b.Path, b.Alias = "b", "/b", "b.test"
	err := u.SaveProject(b)
	if err == nil || strings.Contains(err.Error(), "--disabled") || strings.Contains(err.Error(), "advisory above") ||
		!strings.Contains(err.Error(), `loopback port 5173 is already forwarded for project "a"`) || !strings.Contains(err.Error(), "set this route to ignore") {
		t.Errorf("SaveProject = %v", err)
	}
}

// Discovery applies the project's .nearhostrc, and one that cannot be read
// is reported in the draft, without failing discovery.
func TestFormDiscoveryReadsNearhostrc(t *testing.T) {
	u := formEngine(t)
	ctx := context.Background()
	files, err := u.OpenServer(ctx, config.Server{Name: config.LocalServer})
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	dir := t.TempDir()
	rc := "version: 1\nroutes:\n  - port: 8220\n    hosts: [app.local.dd.io]\n"
	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}
	if !reflect.DeepEqual(d.Routes, want) || d.Nearhostrc == nil || !reflect.DeepEqual(d.Nearhostrc.Routes, want) || d.RcError != "" {
		t.Errorf("draft routes %+v, snapshot %+v, rc error %q", d.Routes, d.Nearhostrc, d.RcError)
	}

	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte("version: 9\nroutes: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err = files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Nearhostrc != nil || !strings.Contains(d.RcError, "update Nearhost") {
		t.Errorf("snapshot %+v, rc error %q", d.Nearhostrc, d.RcError)
	}

	// A file Merge refuses is reported the same way.
	bad := "version: 1\nroutes:\n  - port: 8220\n  - port: 8221\n    hosts: [" + filepath.Base(dir) + ".test]\n"
	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err = files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Nearhostrc != nil || !strings.Contains(d.RcError, "the project's .test name") {
		t.Errorf("snapshot %+v, rc error %q", d.Nearhostrc, d.RcError)
	}
}

// Discovery merges the .nearhostrc for the server the form is on, so a
// local project can ask for docker_clients and a remote one is told it
// cannot.
func TestFormDiscoveryMergesForItsServer(t *testing.T) {
	u := formEngine(t)
	ctx := context.Background()
	dir := t.TempDir()
	rc := "version: 1\ndocker_clients: true\nroutes:\n  - port: 8220\n    hosts: [app.local.dd.io]\n"
	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}

	files := &uiFiles{u: u, srv: config.Server{Name: config.LocalServer}, files: localFiles{}}
	d, err := files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.RcError != "" || d.Nearhostrc == nil || !d.Nearhostrc.DockerClients {
		t.Errorf("local: snapshot %+v, rc error %q", d.Nearhostrc, d.RcError)
	}

	// A remote server's files are read the same way here; only the server
	// the form is on differs.
	files = &uiFiles{u: u, srv: config.Server{Name: "mini"}, files: localFiles{}}
	d, err = files.Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Nearhostrc != nil || !strings.Contains(d.RcError, "docker_clients works only for projects on this machine") || !strings.Contains(d.RcError, `server "mini"`) {
		t.Errorf("remote: snapshot %+v, rc error %q", d.Nearhostrc, d.RcError)
	}
}

// A new project cannot serve a host an enabled project serves; added
// disabled, it serves nothing, so it can.
func TestFormSaveRefusesAHostAnotherProjectServes(t *testing.T) {
	u := formEngine(t)
	route := []config.Route{{Port: 3000, Role: config.RoleWeb, Hosts: []string{"x.local.example.io"}}}
	one := config.Project{Name: "one", Server: config.LocalServer, Path: "/one", Alias: "one.test", Enabled: true, Routes: route}
	if err := u.SaveProject(one); err != nil {
		t.Fatal(err)
	}
	two := config.Project{Name: "two", Server: config.LocalServer, Path: "/two", Alias: "two.test", Enabled: true, Routes: route}
	if err := u.SaveProject(two); err == nil || err.Error() != `host x.local.example.io is already served by project "one"` {
		t.Errorf("SaveProject = %v", err)
	}
	if _, ok := loadSaved(t, u.path).Project("two"); ok {
		t.Error("the clashing project was saved")
	}
	two.Enabled = false
	if err := u.SaveProject(two); err != nil {
		t.Errorf("SaveProject disabled = %v", err)
	}
}

// PublicAddress is the first address DNS gives a host that is not loopback.
func TestFormPublicAddress(t *testing.T) {
	u := &uiEngine{b: uiBackend{lookup: func(_ context.Context, name string) ([]netip.Addr, error) {
		switch name {
		case "app.local.dd.io":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("93.184.216.34")}, nil
		case "loop.dd.io":
			return []netip.Addr{netip.MustParseAddr("::1")}, nil
		}
		return nil, errors.New("no such host")
	}}}
	ctx := context.Background()
	if addr, ok := u.PublicAddress(ctx, "app.local.dd.io"); !ok || addr != "93.184.216.34" {
		t.Errorf("app.local.dd.io = %q, %v", addr, ok)
	}
	for _, h := range []string{"loop.dd.io", "gone.dd.io", "*.dd.io"} {
		if addr, ok := u.PublicAddress(ctx, h); ok {
			t.Errorf("%s = %q, %v", h, addr, ok)
		}
	}
}
