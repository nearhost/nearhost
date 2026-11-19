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
	"github.com/nearhost/nearhost/internal/engine"
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
	if r.Updated != nil || r.Pending != nil || !strings.Contains(r.Problem, `host api.shop.test of project "app" falls under *.shop.test, which project "shop" serves`) {
		t.Fatalf("review = %+v, want a Problem saying api.shop.test falls under shop's pattern", r)
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

// pairRc writes a config where shop serves *.shop.test and api serves
// api.shop.test, a pair saved before it was refused, and gives the named
// project a .nearhostrc with body. It returns that project as approved.
func pairRc(t *testing.T, path, name, body string) config.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nearhostrc.FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	shop := config.Project{Name: "shop", Server: config.LocalServer, Path: "/shop", Alias: "shop.test", Enabled: true,
		Routes: []config.Route{{Port: 4000, Role: config.RoleWeb, Hosts: []string{"*.shop.test"}}}}
	api := config.Project{Name: "api", Server: config.LocalServer, Path: "/api", Alias: "api.test", Enabled: true,
		Routes: []config.Route{{Port: 4001, Role: config.RoleWeb, Hosts: []string{"api.shop.test"}}}}
	projects := []config.Project{shop, api}
	var mine config.Project
	for i := range projects {
		if projects[i].Name == name {
			projects[i].Path = dir
			projects[i].Nearhostrc = &config.RcSnapshot{SHA256: "old", Routes: projects[i].Routes}
			mine = projects[i]
		}
	}
	saveConfig(t, path, &config.Config{Projects: projects})
	return mine
}

// A .nearhostrc change to a project of a pair already in the config is
// taken when it brings in no new clash, and refused with the conflict when
// it does.
func TestReviewKeepsAPairTheConfigHad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	shop := pairRc(t, path, "shop", "version: 1\nroutes:\n  - port: 4000\n    label: Shop\n    hosts: ['*.shop.test']\n")
	r := reviewProject(saveTo(path))(context.Background(), shop, discovery.DirReader{Root: shop.Path})
	if r.Problem != "" || r.Updated == nil {
		t.Fatalf("relabelling shop's route: review = %+v", r)
	}
	if p, _ := loadSaved(t, path).Project("shop"); p.Routes[0].Label != "Shop" {
		t.Errorf("shop's routes were not saved: %+v", p.Routes)
	}

	api := pairRc(t, path, "api", "version: 1\nroutes:\n  - port: 4001\n    hosts: [api.shop.test, www.shop.test]\n")
	r = reviewProject(saveTo(path))(context.Background(), api, discovery.DirReader{Root: api.Path})
	want := `host www.shop.test of project "api" falls under *.shop.test, which project "shop" serves`
	if r.Updated != nil || !strings.Contains(r.Problem, want) || !strings.Contains(r.Problem, "serving what was last approved") {
		t.Fatalf("adding www.shop.test to api: review = %+v, want a Problem with %q", r, want)
	}
}

// `nearhost up` refuses a disabled project named on the command line that
// would complete a pair with an enabled or named project; a pair of enabled
// projects still starts.
func TestUpTargetsRefusesADisabledProjectThatCompletesAPair(t *testing.T) {
	shop := localProject("shop", 8000, true)
	shop.Routes[0].Hosts = []string{"*.shop.test"}
	api := localProject("api", 8001, false)
	api.Routes[0].Hosts = []string{"api.shop.test"}
	cfg := &config.Config{Projects: []config.Project{shop, api}}
	want := `cannot start disabled project "api": host api.shop.test of project "api" falls under *.shop.test, which project "shop" serves`
	if _, err := upTargets(cfg, []string{"shop", "api"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upTargets = %v, want %q", err, want)
	}
	cfg.Projects[0].Enabled = false
	if _, err := upTargets(cfg, []string{"api", "shop"}); err == nil || !strings.Contains(err.Error(), `cannot start disabled project "api"`) {
		t.Errorf("two disabled projects named: upTargets = %v", err)
	}
	if _, err := upTargets(cfg, []string{"api"}); err != nil {
		t.Errorf("api alone, shop disabled and not named: %v", err)
	}
	cfg.Projects[0].Enabled, cfg.Projects[1].Enabled = true, true
	if names, err := upTargets(cfg, []string{"shop", "api"}); err != nil || len(names) != 2 {
		t.Errorf("a pair of enabled projects: upTargets = %v %v", names, err)
	}
}

// Accepting a .nearhostrc change that adds a loopback port to a project of
// a pair already in the config saves it.
func TestAcceptKeepsAPairTheConfigHad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	shop := pairRc(t, path, "shop", "version: 1\n")
	cfg := loadSaved(t, path)
	changed := shop
	changed.Routes = append([]config.Route{}, shop.Routes...)
	changed.Routes = append(changed.Routes, config.Route{Port: 5432, Role: config.RoleLoopback})
	changed.Nearhostrc = &config.RcSnapshot{SHA256: "new", Routes: changed.Routes}
	review := func(context.Context, config.Project, discovery.FileReader) engine.Review {
		return engine.Review{Pending: &engine.Change{SHA256: "new", Project: changed, Items: []nearhostrc.Item{{Port: 5432, Why: "a loopback port"}}}}
	}
	u := &uiEngine{Engine: engine.New(cfg, engine.Options{Review: review}), path: path}
	if err := u.Recheck(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if err := u.AcceptChange("shop", "new"); err != nil {
		t.Fatalf("AcceptChange = %v", err)
	}
	if p, _ := loadSaved(t, path).Project("shop"); len(p.Routes) != 2 {
		t.Errorf("shop's routes were not saved: %+v", p.Routes)
	}
}
