package cli

import (
	"bytes"
	"context"
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

func TestSaveToReplacesTheProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	a := config.Project{Name: "a", Server: config.LocalServer, Path: "/a", Alias: "a.test", Enabled: true,
		Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}
	b := config.Project{Name: "b", Server: config.LocalServer, Path: "/b", Alias: "b.test", Enabled: true,
		Routes: []config.Route{{Port: 4000, Role: config.RoleWeb}}}
	if err := config.Save(path, &config.Config{Projects: []config.Project{a, b}}); err != nil {
		t.Fatal(err)
	}
	a.Routes[0].Port = 3001
	if err := saveTo(path)(a); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 || cfg.Projects[0].Routes[0].Port != 3001 || cfg.Projects[1].Name != "b" {
		t.Fatalf("saved %+v", cfg.Projects)
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
	want := "09:30:00 [app] .nearhostrc asks for hosts that need approval: app.example.com, *.example.com\n" +
		"  open nearhost to approve them; nearhost up cannot\n"
	if out.String() != want {
		t.Fatalf("printed:\n%s\nwant:\n%s", out.String(), want)
	}
}
