package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// A .nearhostrc change that needs no approval, a .test host, is refused when
// the host falls under another enabled project's pattern, and nothing is
// saved.
func TestReviewRefusesAHostUnderAnotherProjectsPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte("version: 1\nroutes:\n  - port: 3001\n    hosts: [api.shop.test]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := config.Project{Name: "app", Server: config.LocalServer, Path: dir, Alias: "app.test", Enabled: true,
		Routes:     []config.Route{{Port: 3000, Role: config.RoleWeb}},
		Nearhostrc: &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}}
	shop := config.Project{Name: "shop", Server: config.LocalServer, Path: "/shop", Alias: "shop.test", Enabled: true,
		Routes: []config.Route{{Port: 4000, Role: config.RoleWeb, Hosts: []string{"*.shop.test"}}}}
	saveConfig(t, path, &config.Config{Projects: []config.Project{app, shop}})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := reviewProject(saveTo(path))(context.Background(), app, discovery.DirReader{Root: dir})
	if r.Updated != nil || r.Pending != nil || !strings.Contains(r.Problem, `host api.shop.test is already served by project "shop"`) {
		t.Fatalf("review = %+v, want a Problem saying api.shop.test is taken", r)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatalf("a refused change was saved:\n%s", after)
	}
}

// A config saved while an exact host under another project's pattern was
// allowed still loads, so Nearhost keeps starting with it.
func TestAConfigWithAnExactHostUnderAPatternStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	shop := localProject("shop", 8000, true)
	shop.Routes[0].Hosts = []string{"*.shop.test"}
	api := localProject("api", 8001, true)
	api.Routes[0].Hosts = []string{"api.shop.test"}
	saveConfig(t, path, &config.Config{Projects: []config.Project{shop, api}})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
