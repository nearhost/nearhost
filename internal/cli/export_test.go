package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/transport"
)

const exportSuccess = "Commit it so others get this setup when they add the project.\n"

// exportDir makes a project directory whose path has no symbolic links in
// it, as os.Getwd reports it, with an .env that sets its web port.
func exportDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(dir, "shop")
	if err := os.MkdirAll(filepath.Join(dir, "app", "Http"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_PORT=3000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// exportProject is a local project at dir serving port 3000 as its web
// route, with one extra host.
func exportProject(name, dir string) config.Project {
	return config.Project{
		Name: name, Server: config.LocalServer, Path: dir, Alias: config.AliasFor(name), Enabled: true,
		Routes: []config.Route{{Port: 3000, Role: config.RoleWeb, Label: "app", Hosts: []string{"api." + name + ".test"}}},
	}
}

// exportConfig saves cfg at a fresh config path and returns it with the bytes
// saved.
func exportConfig(t *testing.T, cfg *config.Config) (string, []byte) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, cfg)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfgPath, before
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// wantSnapshotOf checks that the saved project's snapshot is the file's.
func wantSnapshotOf(t *testing.T, cfgPath, name, dir string) {
	t.Helper()
	f, found, err := nearhostrc.Read(discovery.DirReader{Root: dir})
	if err != nil || !found {
		t.Fatalf("read the written file: %v %v", found, err)
	}
	p, _ := loadSaved(t, cfgPath).Project(name)
	if p.Nearhostrc == nil || p.Nearhostrc.SHA256 != f.SHA256 {
		t.Errorf("snapshot = %+v, want SHA256 %s", p.Nearhostrc, f.SHA256)
	}
}

func TestExportWritesLocalProjectFromSubdirectory(t *testing.T) {
	dir := exportDir(t)
	cfgPath, _ := exportConfig(t, &config.Config{Projects: []config.Project{exportProject("shop", dir)}})
	t.Chdir(filepath.Join(dir, "app", "Http"))

	out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if want := "Wrote " + dir + "/.nearhostrc. " + exportSuccess; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	data := readFile(t, filepath.Join(dir, nearhostrc.FileName))
	if !strings.Contains(string(data), "port: ${APP_PORT:-3000}") {
		t.Errorf("the port does not follow APP_PORT:\n%s", data)
	}
	if info, err := os.Stat(filepath.Join(dir, nearhostrc.FileName)); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v %v, want 0644", info, err)
	}
	wantSnapshotOf(t, cfgPath, "shop", dir)
}

func TestExportPicksDeepestProject(t *testing.T) {
	outer := exportDir(t)
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(filepath.Join(inner, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, _ := exportConfig(t, &config.Config{Projects: []config.Project{exportProject("shop", outer), exportProject("inner", inner)}})
	t.Chdir(filepath.Join(inner, "x"))

	out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	wantContains(t, out, "Wrote "+inner+"/.nearhostrc.")
	if _, err := os.Stat(filepath.Join(outer, nearhostrc.FileName)); !os.IsNotExist(err) {
		t.Errorf("the outer project was written too: %v", err)
	}
	wantSnapshotOf(t, cfgPath, "inner", inner)
	if p, _ := loadSaved(t, cfgPath).Project("shop"); p.Nearhostrc != nil {
		t.Errorf("the outer project's snapshot changed: %+v", p.Nearhostrc)
	}
}

func TestExportOutsideAnyProjectFails(t *testing.T) {
	dir := exportDir(t)
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfgPath, before := exportConfig(t, &config.Config{Projects: []config.Project{exportProject("shop", dir)}})
	t.Chdir(elsewhere)

	_, err = execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export")
	if want := "no Nearhost project contains " + elsewhere + "; add it with nearhost add"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	if !bytes.Equal(readFile(t, cfgPath), before) {
		t.Error("the config changed")
	}
}

func TestExportUnknownProjectFails(t *testing.T) {
	cfgPath, _ := exportConfig(t, &config.Config{})
	_, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "nope")
	if want := `project "nope" is not in ` + cfgPath; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestExportRefusesExistingFileWithoutForce(t *testing.T) {
	dir := exportDir(t)
	cfgPath, before := exportConfig(t, &config.Config{Projects: []config.Project{exportProject("shop", dir)}})
	file := filepath.Join(dir, nearhostrc.FileName)
	hand := []byte("# mine\nversion: 1\nroutes:\n  - port: 3000\n")
	if err := os.WriteFile(file, hand, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop")
	if want := ".nearhostrc already exists in " + dir + "; run with --force to replace it, or --dry-run to see what would be written"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	if !bytes.Equal(readFile(t, file), hand) || !bytes.Equal(readFile(t, cfgPath), before) {
		t.Error("the file or the config changed")
	}

	out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop", "--force")
	if err != nil {
		t.Fatalf("export --force: %v\n%s", err, out)
	}
	if bytes.Equal(readFile(t, file), hand) {
		t.Error("--force left the file as it was")
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the replaced file's 0600", info.Mode().Perm())
	}
	wantSnapshotOf(t, cfgPath, "shop", dir)
}

func TestExportDryRunPrintsAndWritesNothing(t *testing.T) {
	dir := exportDir(t)
	p := exportProject("shop", dir)
	cfgPath, before := exportConfig(t, &config.Config{Projects: []config.Project{p}})
	t.Chdir(dir)

	want, _, err := nearhostrc.Export(p, "shop", nearhostrc.PortVars{3000: {"APP_PORT"}}, map[string]string{"APP_PORT": "3000"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "--dry-run")
	if err != nil {
		t.Fatalf("export --dry-run: %v\n%s", err, out)
	}
	if out != string(want) {
		t.Errorf("output = %q, want the file %q", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, nearhostrc.FileName)); !os.IsNotExist(err) {
		t.Errorf("a dry run wrote the file: %v", err)
	}
	if !bytes.Equal(readFile(t, cfgPath), before) {
		t.Error("a dry run changed the config")
	}

	// An existing file needs no --force to be shown what would replace it.
	hand := []byte("version: 1\nroutes:\n  - port: 3000\n")
	file := filepath.Join(dir, nearhostrc.FileName)
	if err := os.WriteFile(file, hand, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "--dry-run")
	if err != nil || out != string(want) {
		t.Errorf("dry run over a file: %v\n%s", err, out)
	}
	if !bytes.Equal(readFile(t, file), hand) || !bytes.Equal(readFile(t, cfgPath), before) {
		t.Error("a dry run changed the file or the config")
	}
}

// memWriter records what is written through it.
type memWriter struct{ files map[string][]byte }

func (w *memWriter) WriteFile(name string, data []byte) error {
	w.files[name] = append([]byte(nil), data...)
	return nil
}

func TestExportRemoteProjectWritesOverSFTP(t *testing.T) {
	remote := exportDir(t) // stands in for the server's copy of the project
	p := exportProject("box-app", "/srv/www/app")
	p.Server = "box"
	cfgPath, _ := exportConfig(t, &config.Config{
		Servers:  []config.Server{{Name: "box", SSHHost: "box.lan"}},
		Projects: []config.Project{p},
	})
	w := &memWriter{files: map[string][]byte{}}
	var openedSrv config.Server
	var openedPath string
	closed := false
	b := exportBackend{
		openRemote: func(_ context.Context, srv config.Server, projectPath string) (discovery.FileReader, transport.FileWriter, string, func() error, error) {
			openedSrv, openedPath = srv, projectPath
			return discovery.DirReader{Root: remote}, w, projectPath, func() error { closed = true; return nil }, nil
		},
	}

	out, err := execute(t, newExportCmd(b), "--config", cfgPath, "export", "box-app")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if openedSrv.SSHHost != "box.lan" || openedPath != "/srv/www/app" || !closed {
		t.Errorf("opened %+v at %q, closed %v", openedSrv, openedPath, closed)
	}
	data, ok := w.files[nearhostrc.FileName]
	if !ok || !strings.Contains(string(data), "port: ${APP_PORT:-3000}") {
		t.Fatalf("wrote %v", w.files)
	}
	if want := "Wrote /srv/www/app/.nearhostrc on server box. " + exportSuccess; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	f, err := nearhostrc.Parse(data, map[string]string{"APP_PORT": "3000"})
	if err != nil {
		t.Fatal(err)
	}
	if saved, _ := loadSaved(t, cfgPath).Project("box-app"); saved.Nearhostrc == nil || saved.Nearhostrc.SHA256 != f.SHA256 {
		t.Errorf("snapshot = %+v", saved.Nearhostrc)
	}
}

func TestExportCheckFailureLeavesFileUntouched(t *testing.T) {
	dir := exportDir(t)
	p := exportProject("shop", dir)
	p.Routes[0].Label = "two\nlines"
	cfgPath, before := exportConfig(t, &config.Config{Projects: []config.Project{p}})
	file := filepath.Join(dir, nearhostrc.FileName)
	hand := []byte("# mine\nversion: 1\nroutes:\n  - port: 3000\n")
	if err := os.WriteFile(file, hand, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop", "--force")
	if err == nil || !strings.HasPrefix(err.Error(), "export: the label of port 3000 has a line break") {
		t.Errorf("err = %v, want Export's", err)
	}
	if !bytes.Equal(readFile(t, file), hand) || !bytes.Equal(readFile(t, cfgPath), before) {
		t.Error("a failed check changed the file or the config")
	}
}

func TestExportReplacesPendingChange(t *testing.T) {
	dir := exportDir(t)
	p := exportProject("shop", dir)
	// The approved file served port 3000; the one on disk now asks for a
	// real domain, which waits for approval.
	file := filepath.Join(dir, nearhostrc.FileName)
	approved, err := nearhostrc.Parse([]byte("version: 1\nroutes:\n  - port: 3000\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Nearhostrc = &config.RcSnapshot{SHA256: approved.SHA256}
	if err := os.WriteFile(file, []byte("version: 1\nroutes:\n  - port: 3000\n    hosts: [shop.example.com]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath, _ := exportConfig(t, &config.Config{Projects: []config.Project{p}})

	out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop", "--force")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	wantContains(t, out, "replaced the .nearhostrc change that was waiting for approval with what is served now\n", "Wrote "+dir+"/.nearhostrc. "+exportSuccess)
	wantSnapshotOf(t, cfgPath, "shop", dir)

	// With the file now matching its snapshot, nothing is waiting.
	out, err = execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop", "--force")
	if err != nil || strings.Contains(out, "waiting for approval") {
		t.Errorf("second export: %v\n%s", err, out)
	}
}

func TestExportThenStartAsksNothing(t *testing.T) {
	dir := exportDir(t)
	p := exportProject("shop", dir)
	p.Alias = "shop.example.com"
	p.Routes = append(p.Routes, config.Route{Port: 5173, Role: config.RoleLoopback, Label: "vite"})
	cfgPath, _ := exportConfig(t, &config.Config{Projects: []config.Project{p}})

	if out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop"); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	saved, _ := loadSaved(t, cfgPath).Project("shop")
	review := reviewProject(func(config.Project) error {
		t.Error("the review saved a change")
		return nil
	})
	r := review(context.Background(), *saved, discovery.DirReader{Root: dir})
	if r.Pending != nil || r.Problem != "" || r.Updated != nil {
		t.Errorf("review after export = %+v", r)
	}
}

// TestExportReportsPendingAsTheReviewDoes checks that export says it replaced
// a change waiting for approval only when the review would have held that
// change for approval, with an approved snapshot or without one.
func TestExportReportsPendingAsTheReviewDoes(t *testing.T) {
	const pendingLine = "replaced the .nearhostrc change that was waiting for approval"
	approved, err := nearhostrc.Parse([]byte("version: 1\nroutes:\n  - port: 3000\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		snapshot *config.RcSnapshot
		file     string
		pending  bool
	}{
		{"port only, approved before", &config.RcSnapshot{SHA256: approved.SHA256}, "version: 1\nroutes:\n  - port: 3001\n", false},
		{"real domain, approved before", &config.RcSnapshot{SHA256: approved.SHA256}, "version: 1\nroutes:\n  - port: 3000\n    hosts: [shop.example.com]\n", true},
		{"port only, never approved", nil, "version: 1\nroutes:\n  - port: 3001\n", false},
		{"real domain, never approved", nil, "version: 1\nroutes:\n  - port: 3000\n    hosts: [shop.example.com]\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := exportDir(t)
			p := exportProject("shop", dir)
			p.Nearhostrc = c.snapshot
			if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte(c.file), 0o644); err != nil {
				t.Fatal(err)
			}
			cfgPath, _ := exportConfig(t, &config.Config{Projects: []config.Project{p}})

			out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop", "--force")
			if err != nil {
				t.Fatalf("export: %v\n%s", err, out)
			}
			if got := strings.Contains(out, pendingLine); got != c.pending {
				t.Errorf("pending line shown = %t, want %t; output:\n%s", got, c.pending, out)
			}
			wantSnapshotOf(t, cfgPath, "shop", dir)
		})
	}
}

// TestExportThenAddOnAnotherMachine is the seam between export and add: a
// project exported here, then added from its folder with another .env, as
// whoever clones it would, comes back as the same project, serving the port
// the new .env sets.
func TestExportThenAddOnAnotherMachine(t *testing.T) {
	dir := exportDir(t)
	p := exportProject("shop", dir)
	p.Alias = "shop.example.com"
	p.Routes = []config.Route{
		{Port: 3000, Role: config.RoleWeb, Label: "app", Hosts: []string{"api.shop.test"}},
		{Port: 5173, Role: config.RoleLoopback, Label: "vite"},
		{Port: 5432, Role: config.RoleIgnore, Label: "postgres"},
	}
	p.Nearhostrc = &config.RcSnapshot{DockerClients: true}
	cfgPath, _ := exportConfig(t, &config.Config{Projects: []config.Project{p}})

	if out, err := execute(t, newExportCmd(exportBackend{}), "--config", cfgPath, "export", "shop"); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_PORT=3100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveConfig(t, cfgPath, &config.Config{})
	out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "shop.example.com", "docker_clients", "Approve these? [y/N] ")

	got, ok := loadSaved(t, cfgPath).Project("shop")
	if !ok {
		t.Fatalf("the project was not added:\n%s", out)
	}
	if got.Alias != "shop.example.com" {
		t.Errorf("main name = %q, want shop.example.com", got.Alias)
	}
	if !got.DockerClients() {
		t.Error("docker_clients is off")
	}
	want := p.Routes
	want[0].Port = 3100
	if len(got.Routes) != len(want) {
		t.Fatalf("routes = %+v, want %+v", got.Routes, want)
	}
	for i, w := range want {
		g := got.Routes[i]
		if g.Port != w.Port || g.Role != w.Role || g.Label != w.Label || !slices.Equal(g.Hosts, w.Hosts) {
			t.Errorf("route %d = %+v, want %+v", i, g, w)
		}
	}
}

// A project changed by another process while it was exported is not what
// the file was rendered from, so the file is not approved.
func TestExportDoesNotApproveAProjectChangedMeanwhile(t *testing.T) {
	remote := exportDir(t)
	p := exportProject("box-app", "/srv/www/app")
	p.Server = "box"
	cfgPath, _ := exportConfig(t, &config.Config{
		Servers:  []config.Server{{Name: "box", SSHHost: "box.lan"}},
		Projects: []config.Project{p},
	})
	w := &memWriter{files: map[string][]byte{}}
	b := exportBackend{
		openRemote: func(_ context.Context, srv config.Server, projectPath string) (discovery.FileReader, transport.FileWriter, string, func() error, error) {
			if err := config.Update(cfgPath, func(cfg *config.Config) error {
				p, _ := cfg.Project("box-app")
				p.Routes = append(p.Routes, config.Route{Port: 5173, Role: config.RoleIgnore})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return discovery.DirReader{Root: remote}, w, projectPath, func() error { return nil }, nil
		},
	}
	out, err := execute(t, newExportCmd(b), "--config", cfgPath, "export", "box-app")
	if err == nil || !strings.Contains(err.Error(), "changed while it was exported") {
		t.Fatalf("export = %v, want the project changed\n%s", err, out)
	}
	saved, _ := loadSaved(t, cfgPath).Project("box-app")
	if saved.Nearhostrc != nil || len(saved.Routes) != 2 {
		t.Errorf("saved %+v, want the other change and no snapshot", saved)
	}
}
