package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

func TestReviewProjectCases(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := discovery.DirReader{Root: dir}
	var saved []config.Project
	review := reviewProject(func(p config.Project) error {
		saved = append(saved, p)
		return nil
	})
	// approved is the project as it is after approving the file on disk.
	approved := func() config.Project {
		t.Helper()
		f, found, err := nearhostrc.Read(files)
		if err != nil || !found {
			t.Fatalf("Read: %v %v", found, err)
		}
		p := config.Project{Name: "app", Server: config.LocalServer, Path: dir, Alias: "app.test", Enabled: true}
		p, err = nearhostrc.Merge(p, f, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()

	write("version: 1\nroutes:\n  - port: 3000\n    hosts: [app.test]\n")
	p := approved()
	if r := review(ctx, p, files); !reflect.DeepEqual(r, engine.Review{}) {
		t.Fatalf("unchanged hash: %+v", r)
	}

	write("version: 1\nroutes:\n  - port: 3001\n    hosts: [app.test]\n")
	r := review(ctx, p, files)
	if r.Updated == nil || r.Pending != nil || r.Problem != "" || r.Updated.Routes[0].Port != 3001 {
		t.Fatalf("port moved: %+v", r)
	}
	if len(saved) != 1 || saved[0].Routes[0].Port != 3001 {
		t.Fatalf("saved %+v", saved)
	}

	saved = nil
	write("version: 1\nroutes:\n  - port: 3000\n    hosts: [app.test, app.example.com]\n")
	r = review(ctx, p, files)
	want := []nearhostrc.Item{{Port: 3000, Host: "app.example.com", Why: "a real domain"}}
	if r.Pending == nil || r.Updated != nil || !reflect.DeepEqual(r.Pending.Items, want) {
		t.Fatalf("real-domain host: %+v", r)
	}
	if r.Pending.Project.Routes[0].Hosts[1] != "app.example.com" || len(r.Pending.SHA256) != 64 {
		t.Fatalf("pending change: %+v", r.Pending)
	}
	if len(saved) != 0 {
		t.Fatalf("a change that needs approval was saved: %+v", saved)
	}

	write("version: 1\nroutes:\n  - port: 3000\n    role: nonsense\n")
	if r := review(ctx, p, files); !strings.HasPrefix(r.Problem, ".nearhostrc") || r.Pending != nil || r.Updated != nil {
		t.Fatalf("parse error: %+v", r)
	}

	if err := os.Remove(filepath.Join(dir, nearhostrc.FileName)); err != nil {
		t.Fatal(err)
	}
	if r := review(ctx, p, files); r.Problem != ".nearhostrc is gone; serving what was last approved" {
		t.Fatalf("file deleted: %+v", r)
	}
	p.Nearhostrc = nil
	if r := review(ctx, p, files); !reflect.DeepEqual(r, engine.Review{}) {
		t.Fatalf("no file, no snapshot: %+v", r)
	}
	if len(saved) != 0 {
		t.Fatalf("saved %+v", saved)
	}
}

// Turning docker_clients off needs no approval, so it is saved; a save
// that fails says so without calling it a port or label change.
func TestReviewSaysWhenSavingAChangeWithoutApprovalFails(t *testing.T) {
	dir := t.TempDir()
	files := discovery.DirReader{Root: dir}
	p := config.Project{Name: "app", Server: config.LocalServer, Path: dir, Alias: "app.test", Enabled: true,
		Routes:     []config.Route{{Port: 3000, Role: config.RoleWeb}},
		Nearhostrc: &config.RcSnapshot{SHA256: "old", DockerClients: true, Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}}
	if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte("version: 1\nroutes:\n  - port: 3000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	review := reviewProject(func(config.Project) error { return errors.New("disk full") })
	r := review(context.Background(), p, files)
	const want = ".nearhostrc changed nothing that needs approval, but saving the config failed: disk full"
	if r.Problem != want || r.Pending != nil || r.Updated != nil {
		t.Fatalf("review = %+v, want Problem %q", r, want)
	}
}

func TestSaveToReplacesTheProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	a := config.Project{Name: "a", Server: config.LocalServer, Path: "/a", Alias: "a.test", Enabled: true,
		Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}
	b := config.Project{Name: "b", Server: config.LocalServer, Path: "/b", Alias: "b.test", Enabled: true,
		Routes: []config.Route{{Port: 4000, Role: config.RoleWeb}}}
	if err := config.Save(path, &config.Config{Projects: []config.Project{a, b}}); err != nil {
		t.Fatal(err)
	}
	// The engine's copy of a is what was loaded; the file changes after.
	stale := a
	stale.Routes = []config.Route{{Port: 3001, Role: config.RoleWeb}}
	stale.Nearhostrc = &config.RcSnapshot{SHA256: "abc", Routes: []config.Route{{Port: 3001, Role: config.RoleWeb}}}
	edited := a
	edited.Enabled, edited.Alias = false, "renamed.test"
	if err := config.Save(path, &config.Config{Projects: []config.Project{edited, b}}); err != nil {
		t.Fatal(err)
	}
	if err := saveTo(path)(stale); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Projects[0]
	if len(cfg.Projects) != 2 || got.Routes[0].Port != 3001 || got.Nearhostrc == nil || got.Nearhostrc.SHA256 != "abc" || cfg.Projects[1].Name != "b" {
		t.Fatalf("saved %+v", cfg.Projects)
	}
	if got.Enabled || got.Alias != "renamed.test" {
		t.Fatalf("the save undid an edit made to the file since: %+v", got)
	}
	gone := config.Project{Name: "gone", Server: config.LocalServer, Path: "/g", Alias: "g.test"}
	if err := saveTo(path)(gone); err == nil {
		t.Fatal("saving a project that is no longer configured succeeded")
	}
}

func TestUpPrintsWhereToApproveAPendingChange(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{{Name: "app", Server: config.LocalServer, Path: "/a", Alias: "app.test"}}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), cfg: cfg}
	at := time.Date(2026, 10, 16, 9, 30, 0, 0, time.Local)
	p.event(engine.Event{Time: at, Project: "app", Message: engine.PendingPrefix + "app.example.com, *.example.com"})
	want := "09:30:00 [app] .nearhostrc asks for changes that need approval: app.example.com, *.example.com\n" +
		"  open nearhost to approve them; nearhost up cannot\n"
	if out.String() != want {
		t.Fatalf("printed:\n%s\nwant:\n%s", out.String(), want)
	}
}

// clashSetup writes a config in which app's .nearhostrc moves its port and
// also lists other.test, the alias of another enabled project. The change
// needs no approval, since the new host is under .test, so the review saves
// it, and the save must refuse it.
func clashSetup(t *testing.T, path string) config.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte("version: 1\nroutes:\n  - port: 3001\n    hosts: [other.test]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := config.Project{Name: "app", Server: config.LocalServer, Path: dir, Alias: "app.test", Enabled: true,
		Routes:     []config.Route{{Port: 3000, Role: config.RoleWeb}},
		Nearhostrc: &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}}
	other := config.Project{Name: "other", Server: config.LocalServer, Path: "/other", Alias: "other.test", Enabled: true,
		Routes: []config.Route{{Port: 4000, Role: config.RoleWeb}}}
	cfg := &config.Config{}
	if _, err := os.Stat(path); err == nil {
		cfg = loadSaved(t, path)
	}
	cfg.Projects = append(cfg.Projects, app, other)
	saveConfig(t, path, cfg)
	return app
}

func checkClashRefused(t *testing.T, path string, save func(config.Project) error, app config.Project) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := reviewProject(save)(context.Background(), app, discovery.DirReader{Root: app.Path})
	if r.Updated != nil || r.Pending != nil || !strings.Contains(r.Problem, `host other.test is already served by project "other"`) {
		t.Fatalf("review = %+v, want a Problem saying other.test is taken", r)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatalf("a refused change was saved:\n%s", after)
	}
}

func TestSaveToRefusesAHostAnotherProjectServes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	app := clashSetup(t, path)
	checkClashRefused(t, path, saveTo(path), app)
}

func TestSaveRcRefusesAHostAnotherProjectServes(t *testing.T) {
	u := formEngine(t)
	app := clashSetup(t, u.path)
	checkClashRefused(t, u.path, u.saveRc, app)
}

// rcAliasSetup writes a config in which app, approved with alias a.test from
// its .nearhostrc, now has a file whose alias is next. It returns the
// project as the engine has it.
func rcAliasSetup(t *testing.T, path, next string, others ...config.Project) config.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte("version: 1\nalias: "+next+"\nroutes:\n  - port: 3000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := config.Project{Name: "app", Server: config.LocalServer, Path: dir, Alias: "a.test", Enabled: true,
		Routes:     []config.Route{{Port: 3000, Role: config.RoleWeb}},
		Nearhostrc: &config.RcSnapshot{SHA256: "old", Alias: "a.test", Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}}
	saveConfig(t, path, &config.Config{Projects: append([]config.Project{app}, others...)})
	return app
}

// A start's review holds a main name that moves to a real domain for
// approval, as it does a real-domain host, and saves nothing; accepting it
// saves the new main name.
func TestReviewHoldsRealDomainAliasChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	app := rcAliasSetup(t, path, "app.local.x.io")
	r := reviewProject(saveTo(path))(context.Background(), app, discovery.DirReader{Root: app.Path})
	want := []nearhostrc.Item{{Port: 3000, Host: "app.local.x.io", Why: "a real domain"}}
	if r.Pending == nil || r.Updated != nil || r.Problem != "" || !reflect.DeepEqual(r.Pending.Items, want) {
		t.Fatalf("review = %+v", r)
	}
	if r.Pending.Project.Alias != "app.local.x.io" {
		t.Fatalf("pending project alias %q", r.Pending.Project.Alias)
	}
	if p, _ := loadSaved(t, path).Project("app"); p.Alias != "a.test" || p.Nearhostrc.Alias != "a.test" {
		t.Fatalf("saved alias %q, snapshot %+v", p.Alias, p.Nearhostrc)
	}
	cfg := loadSaved(t, path)
	if err := takeRc(cfg, r.Pending.Project, path); err != nil {
		t.Fatal(err)
	}
	if p, _ := cfg.Project("app"); p.Alias != "app.local.x.io" || p.Nearhostrc.Alias != "app.local.x.io" {
		t.Fatalf("accepted alias %q, snapshot %+v", p.Alias, p.Nearhostrc)
	}
}

// A main name that moves under .test needs no approval and is saved; one
// another enabled project serves is refused like a host would be.
func TestReviewRefusesFileAliasClash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	other := config.Project{Name: "other", Server: config.LocalServer, Path: "/other", Alias: "other.test", Enabled: true,
		Routes: []config.Route{{Port: 4000, Role: config.RoleWeb, Hosts: []string{"shared.test"}}}}
	app := rcAliasSetup(t, path, "shared.test", other)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := reviewProject(saveTo(path))(context.Background(), app, discovery.DirReader{Root: app.Path})
	if r.Updated != nil || r.Pending != nil || !strings.Contains(r.Problem, `host shared.test is already served by project "other"`) {
		t.Fatalf("review = %+v", r)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatalf("a refused change was saved:\n%s", after)
	}

	moved := filepath.Join(t.TempDir(), "config.yaml")
	app = rcAliasSetup(t, moved, "b.test", other)
	r = reviewProject(saveTo(moved))(context.Background(), app, discovery.DirReader{Root: app.Path})
	if r.Updated == nil || r.Updated.Alias != "b.test" {
		t.Fatalf("review = %+v", r)
	}
	if p, _ := loadSaved(t, moved).Project("app"); p.Alias != "b.test" || p.Nearhostrc.Alias != "b.test" {
		t.Fatalf("saved alias %q, snapshot %+v", p.Alias, p.Nearhostrc)
	}
}
