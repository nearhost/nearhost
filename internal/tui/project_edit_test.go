package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
)

// editModel is a model with the form open to edit myapp, whose files on
// mini now hold ports 80 and 8025 (3306 and 5173 are no longer found).
func editModel(t *testing.T) (*Model, *fakeBackend, *fakeFiles) {
	t.Helper()
	f := newFake(t, sampleConfig())
	files := &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/srv/myapp": true}, drafts: map[string]Draft{
		"/srv/myapp": {Routes: []config.Route{
			{Port: 80, Role: config.RoleWeb, Label: "app"},
			{Port: 8025, Role: config.RoleWeb, Label: "mail"},
		}},
	}}
	f.forms.files = files
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	settle(m, press(m, "e"))
	return m, f, files
}

func TestEditProjectKeepsSavedRoutes(t *testing.T) {
	m, _, _ := editModel(t)
	wantScreen(t, m, "Edit myapp · routes",
		"[x] 80      ‹ web ›       https://myapp.test  web",  // saved role and label kept
		"[ ] 8025    ignore        –                   mail", // new: does not take the web route
		"[x] 5173    loopback      localhost:5173      vite (VITE_PORT)",
		"[ ] 3306    ignore        –                   db")
	// The marks are at the end of their own rows.
	wantRow(t, m, "[x] 5173", "vite (VITE_PORT)", "not found now   │")
	wantRow(t, m, "[ ] 3306", "db", "not found now   │")
}

func TestEditProjectRenamesAndSaves(t *testing.T) {
	m, f, files := editModel(t)
	press(m, "enter") // routes
	wantScreen(t, m, "Edit myapp · name", "name      myapp▏", "alias     https://myapp.test")
	// Its own name and alias are not taken by itself.
	f.forms.nameErr = errors.New(`a project called "myapp" already exists, on mini`)
	f.forms.aliasErr = errors.New(`alias myapp.test is already used by project "myapp"`)
	typeText(m, "2")
	press(m, "ctrl+u")
	typeText(m, "myapp")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit myapp · confirm", "enter save · esc back")
	press(m, "esc")
	press(m, "ctrl+u")
	typeText(m, "shop")
	f.forms.nameErr, f.forms.aliasErr = nil, nil
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	p, ok := f.cfg.Project("shop")
	if !ok || p.Alias != "shop.test" || p.Server != "mini" || p.Path != "/srv/myapp" || len(p.Routes) != 4 {
		t.Fatalf("saved %+v %v", p, ok)
	}
	if _, still := f.cfg.Project("myapp"); still {
		t.Error("the old name is still configured")
	}
	if f.cfg.Projects[1].Name != "shop" {
		t.Errorf("the project moved: %+v", f.cfg.Projects)
	}
	if m.form != nil || m.sel != rowKey(rowProject, "shop") || files.closed != 1 {
		t.Errorf("form %T, selected %q, files closed %d", m.form, m.sel, files.closed)
	}
	wantScreen(t, m, "Saved shop.")
}

func TestEditProjectCancels(t *testing.T) {
	m, f, files := editModel(t)
	settle(m, press(m, "esc"))
	if m.form != nil || files.closed != 1 || f.edit.edits != 0 {
		t.Errorf("form %T, files closed %d, edits %d", m.form, files.closed, f.edit.edits)
	}
}

func TestRemoveProject(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	press(m, "d")
	wantScreen(t, m, "Remove myapp?", "It stops now and leaves the config.", "y remove · n keep")
	settle(m, press(m, "n"))
	if m.form != nil || len(f.cfg.Projects) != 3 {
		t.Fatalf("n: form %T, %d projects", m.form, len(f.cfg.Projects))
	}
	press(m, "d")
	f.edit.editErr = errors.New("write config.yaml: permission denied")
	settle(m, press(m, "y"))
	wantScreen(t, m, "Write config.yaml: permission denied", "y remove · n keep")
	f.edit.editErr = nil
	settle(m, press(m, "y"))
	if _, ok := f.cfg.Project("myapp"); ok || m.form != nil {
		t.Errorf("myapp still configured, or form %T open", m.form)
	}
	if m.sel != rowKey(rowServer, "mini") {
		t.Errorf("selected %q", m.sel)
	}
	wantScreen(t, m, "Removed myapp.")
}

// TestEditProjectKeepsHostsOfEveryWebRoute edits a project with two web
// routes, each with hand-edited hosts: renaming it must keep both routes
// web and keep their hosts, since the routes step starts from discovery.
func TestEditProjectKeepsHostsOfEveryWebRoute(t *testing.T) {
	cfg := sampleConfig()
	cfg.Projects[1].Routes = []config.Route{
		{Port: 8220, Role: config.RoleWeb, Label: "app", Hosts: []string{"app.local.x.io"}},
		{Port: 8230, Role: config.RoleWeb, Label: "ws", Hosts: []string{"ws.local.x.io"}},
	}
	f := newFake(t, cfg)
	f.forms.files = &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/srv/myapp": true}, drafts: map[string]Draft{
		"/srv/myapp": {Routes: []config.Route{
			{Port: 8220, Role: config.RoleWeb, Label: "app"},
			{Port: 8230, Role: config.RoleIgnore, Label: "ws"},
		}},
	}}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	settle(m, press(m, "e"))
	press(m, "enter") // routes
	press(m, "ctrl+u")
	typeText(m, "shop")
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	p, ok := f.cfg.Project("shop")
	if !ok || len(p.Routes) != 2 {
		t.Fatalf("saved %+v %v\n%s", p, ok, screen(m))
	}
	want := map[int]string{8220: "app.local.x.io", 8230: "ws.local.x.io"}
	for _, r := range p.Routes {
		if r.Role != config.RoleWeb || len(r.Hosts) != 1 || r.Hosts[0] != want[r.Port] {
			t.Errorf("route %d: role %s, hosts %v; want web, [%s]", r.Port, r.Role, r.Hosts, want[r.Port])
		}
	}
}

// TestMakingARouteWebKeepsTheOtherWebRoutes makes a third route web: the
// others stay web, so cycling roles changes nothing else, and the new one
// without hosts becomes the primary.
func TestMakingARouteWebKeepsTheOtherWebRoutes(t *testing.T) {
	f := &projectForm{routes: []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8230, Role: config.RoleWeb, Hosts: []string{"ws.local.x.io"}},
		{Port: 9000, Role: config.RoleIgnore},
	}, routeRow: 2}
	f.setRole(2, config.RoleWeb)
	var got []string
	for _, r := range f.routes {
		got = append(got, fmt.Sprintf("%d %s", r.Port, r.Role))
	}
	if strings.Join(got, ", ") != "9000 web, 80 web, 8230 web" || f.routeRow != 0 {
		t.Errorf("routes %v, row %d", got, f.routeRow)
	}
}

// A web route cycled to another role and back gets its hosts again, but not
// one another route has taken meanwhile.
func TestCyclingARouteBackToWebKeepsItsHosts(t *testing.T) {
	f := &projectForm{srv: config.Server{Name: "mini"}, routes: []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"ws.x.test", "api.x.test"}},
	}, routeRow: 1}
	f.cycleRole(1, 1)
	if r := f.routes[1]; r.Role != config.RoleLoopback || len(r.Hosts) != 0 {
		t.Fatalf("route %+v; want loopback without hosts", r)
	}
	f.routes[0].Hosts = []string{"api.x.test"}
	f.cycleRole(1, 1)
	f.cycleRole(1, 1)
	if r := f.routes[1]; r.Role != config.RoleWeb || strings.Join(r.Hosts, " ") != "ws.x.test" {
		t.Errorf("route %+v; want web with ws.x.test", r)
	}
}

// Enter makes the web route without hosts the primary when one with hosts
// is above it, as only the primary goes without, and the cursor stays on
// the route it was on.
func TestRoutesEnterMakesTheBareWebRouteThePrimary(t *testing.T) {
	f := &projectForm{srv: config.Server{Name: "mini"}, routes: []config.Route{
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"ws.x.test"}},
		{Port: 80, Role: config.RoleWeb},
	}, step: projectStepRoutes}
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.err != "" || f.step != projectStepDetails || f.routes[0].Port != 80 || f.routeRow != 1 {
		t.Errorf("err %q, step %v, routes %+v, row %d", f.err, f.step, f.routes, f.routeRow)
	}
}

// Saving an edit that serves a host another enabled project serves fails
// in the form, and nothing is saved.
func TestEditSaveRefusesAHostAnotherProjectServes(t *testing.T) {
	m, f, _ := editModel(t)
	f.cfg.Projects[0].Routes[0].Hosts = []string{"x.local.example.io"} // website
	press(m, "h")
	typeText(m, "x.local.example.io")
	press(m, "enter")
	press(m, "enter")
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit myapp · confirm", `Host x.local.example.io is already served by project "website"`)
	if f.edit.edits != 0 || m.form == nil {
		t.Errorf("edits %d, form %T", f.edit.edits, m.form)
	}
}

// Editing a project with a .nearhostrc keeps the hosts added by hand to a
// port the file lists, takes the file's own, and asks only about the new.
func TestEditProjectMergesNearhostrc(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.cfg.Projects[1].Routes[0].Hosts = []string{"old.local.dd.io", "mine.local.dd.io"}
	f.cfg.Projects[1].Nearhostrc = &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"old.local.dd.io"}}}}
	rc := &config.RcSnapshot{SHA256: "new", Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Label: "app", Hosts: []string{"new.local.dd.io"}}}}
	f.forms.files = &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/srv/myapp": true}, drafts: map[string]Draft{
		"/srv/myapp": {Routes: []config.Route{
			{Port: 80, Role: config.RoleWeb, Label: "app", Hosts: []string{"new.local.dd.io"}},
			{Port: 8025, Role: config.RoleIgnore, Label: "mail"},
		}, Nearhostrc: rc},
	}}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	settle(m, press(m, "e"))
	pf := m.form.(*projectForm)
	if h := pf.routes[0].Hosts; pf.routes[0].Port != 80 || len(h) != 2 || h[0] != "new.local.dd.io" || h[1] != "mine.local.dd.io" {
		t.Fatalf("routes %+v", pf.routes)
	}
	wantScreen(t, m, "from .nearhostrc")
	press(m, "enter")
	// The name and alias are kept: the backend says another project has
	// them, which is this one, and that must not hide what needs approval.
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit myapp · confirm", "Needs your approval:", "new.local.dd.io on port 80 (a real domain)", "enter approve and save")
	if strings.Contains(screen(m), "mine.local.dd.io on port") {
		t.Errorf("a saved host is asked about:\n%s", screen(m))
	}
	if len(pf.advice) == 0 {
		t.Error("keeping the name skipped the advice")
	}
	if p, _ := f.cfg.Project("myapp"); f.edit.edits != 0 || p.Nearhostrc.SHA256 != "old" {
		t.Fatalf("saved before enter: edits %d, snapshot %+v", f.edit.edits, p.Nearhostrc)
	}
	settle(m, press(m, "enter"))
	p, _ := f.cfg.Project("myapp")
	if p.Nearhostrc == nil || p.Nearhostrc.SHA256 != "new" {
		t.Errorf("saved snapshot %+v", p.Nearhostrc)
	}
	if h := p.Routes[0].Hosts; len(h) != 2 || h[0] != "new.local.dd.io" || h[1] != "mine.local.dd.io" {
		t.Errorf("saved hosts %v", h)
	}
}

// Editing a project whose .nearhostrc cannot be read now saves it without
// the approved snapshot, so the next read that works is reviewed in full
// rather than compared with what was approved before the file broke.
func TestEditProjectWithAnUnreadableNearhostrcClearsTheSnapshot(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.cfg.Projects[1].Nearhostrc = &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}}
	f.forms.files = &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/srv/myapp": true}, drafts: map[string]Draft{
		"/srv/myapp": {Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Label: "app"}}, RcError: ".nearhostrc: version 0 is not valid"},
	}}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	settle(m, press(m, "e"))
	wantScreen(t, m, ".nearhostrc: version 0 is not valid")
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit myapp · confirm")
	settle(m, press(m, "enter"))
	p, _ := f.cfg.Project("myapp")
	if f.edit.edits != 1 || p.Nearhostrc != nil {
		t.Fatalf("edits %d, saved snapshot %+v, want none", f.edit.edits, p.Nearhostrc)
	}
}

// Editing a project that was approved for docker_clients keeps the opt-in
// through mergeRc and asks about nothing.
func TestEditKeepsDockerClientsThroughMergeRc(t *testing.T) {
	f := newFake(t, sampleConfig())
	snap := func() *config.RcSnapshot {
		return &config.RcSnapshot{SHA256: "abc", DockerClients: true, Routes: []config.Route{{Port: 8080, Role: config.RoleWeb}}}
	}
	f.cfg.Projects[0].Nearhostrc = snap()
	f.forms.files = &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/code/website": true}, drafts: map[string]Draft{
		"/code/website": {Routes: []config.Route{{Port: 8080, Role: config.RoleWeb}}, Nearhostrc: snap()},
	}}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "website"))
	settle(m, press(m, "e"))
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit website · confirm")
	if s := screen(m); strings.Contains(s, "Needs your approval:") || strings.Contains(s, "docker_clients") {
		t.Errorf("an approved opt-in is asked about again:\n%s", s)
	}
	settle(m, press(m, "enter"))
	if p, _ := f.cfg.Project("website"); f.edit.edits != 1 || !p.DockerClients() {
		t.Fatalf("edits %d, saved %+v", f.edit.edits, p.Nearhostrc)
	}
}

// Editing a project whose .nearhostrc moved its main name takes the new
// one, as a start would; a name given by hand while the file kept its own
// stays.
func TestEditTakesTheMainNameTheNearhostrcMoved(t *testing.T) {
	for _, tc := range []struct {
		name, alias, snapAlias, want string
	}{
		{"file moved it", "a.test", "a.test", "b.test"},
		{"hand edit kept", "mine.test", "b.test", "mine.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, sampleConfig())
			routes := []config.Route{{Port: 8080, Role: config.RoleWeb}}
			f.cfg.Projects[0].Alias = tc.alias
			f.cfg.Projects[0].Nearhostrc = &config.RcSnapshot{SHA256: "old", Alias: tc.snapAlias, Routes: routes}
			f.forms.files = &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/code/website": true}, drafts: map[string]Draft{
				"/code/website": {Name: "website", Alias: "b.test", Routes: routes, Nearhostrc: &config.RcSnapshot{SHA256: "new", Alias: "b.test", Routes: routes}},
			}}
			m := sized(f)
			m.selectRow(rowKey(rowProject, "website"))
			settle(m, press(m, "e"))
			press(m, "enter")
			wantScreen(t, m, "alias     https://"+tc.want)
			settle(m, press(m, "enter"))
			settle(m, press(m, "enter"))
			if p, _ := f.cfg.Project("website"); p.Alias != tc.want || p.Nearhostrc.Alias != "b.test" {
				t.Fatalf("saved alias %q, snapshot %+v", p.Alias, p.Nearhostrc)
			}
		})
	}
}

// An agent's proposed web port takes the place of the web route without
// hosts only: that one is ignored, a hosted web route stays web, and the
// proposed port becomes the primary so the project still validates.
func TestProposeWebKeepsWebRoutesWithHosts(t *testing.T) {
	f := &projectForm{routes: []config.Route{
		{Port: 3000, Role: config.RoleWeb},
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"ws.x.io"}},
	}}
	f.proposeWeb(8080)
	want := []config.Route{
		{Port: 3000, Role: config.RoleIgnore},
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"ws.x.io"}},
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
	}
	if !reflect.DeepEqual(f.routes, want) {
		t.Fatalf("routes = %+v, want %+v", f.routes, want)
	}
	f.proposeWeb(80)
	if f.routes[1].Port != 80 || f.routes[2].Role != config.RoleWeb {
		t.Fatalf("routes = %+v, want 80 the primary and 8080 still web", f.routes)
	}
	cfg := &config.Config{Projects: []config.Project{{Name: "x", Server: config.LocalServer, Alias: "x.test", Enabled: true, Routes: f.routes}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// A port without hosts made web while a hosted web route comes first would
// be a second web route without hosts. It becomes the primary instead, and
// the cursor stays on it.
func TestMakingAHostlessRouteWebMakesItPrimary(t *testing.T) {
	f := &projectForm{routeRow: 1, routes: []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
		{Port: 9000, Role: config.RoleIgnore},
	}}
	f.setRole(1, config.RoleWeb)
	want := []config.Route{
		{Port: 9000, Role: config.RoleWeb},
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
	}
	if !reflect.DeepEqual(f.routes, want) || f.routeRow != 0 {
		t.Fatalf("routes = %+v, row %d; want %+v, row 0", f.routes, f.routeRow, want)
	}
	cfg := &config.Config{Projects: []config.Project{{Name: "x", Server: config.LocalServer, Alias: "x.test", Enabled: true, Routes: f.routes}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// A web route with hosts that is ignored loses its hosts, which only a web
// route has, so the project still saves.
func TestIgnoringAHostedRouteDropsItsHosts(t *testing.T) {
	f := &projectForm{routes: []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"files.x.io"}},
	}}
	f.setRole(1, config.RoleIgnore)
	want := []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8080, Role: config.RoleIgnore},
	}
	if !reflect.DeepEqual(f.routes, want) {
		t.Fatalf("routes = %+v, want %+v", f.routes, want)
	}
	cfg := &config.Config{Projects: []config.Project{{Name: "x", Server: config.LocalServer, Alias: "x.test", Enabled: true, Routes: f.routes}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// An alias chosen after the routes step that a route other than the primary
// names is served on the primary only, so the project still saves.
func TestFormProjectDropsTheAliasFromSecondaryRoutes(t *testing.T) {
	f := &projectForm{routes: []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"Ws.x.io"}},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"ws.x.io", "files.x.io"}},
	}}
	f.name.value, f.alias.value = "x", "ws.x.io"
	p := f.project()
	want := []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8080, Role: config.RoleIgnore},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"files.x.io"}},
	}
	if !reflect.DeepEqual(p.Routes, want) {
		t.Fatalf("routes = %+v, want %+v", p.Routes, want)
	}
	if len(f.routes[1].Hosts) != 1 {
		t.Errorf("changed the form's routes: %+v", f.routes)
	}
}

// The last look shows the routes as they are saved, without the alias on a
// route other than the primary.
func TestConfirmShowsTheRoutesAsSaved(t *testing.T) {
	f := &projectForm{step: projectStepConfirm, srv: config.Server{Name: config.LocalServer}, routes: []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"ws.x.io"}},
	}}
	f.name.value, f.alias.value = "x", "ws.x.io"
	screen := ansi.Strip(strings.Join(f.lines(100), "\n"))
	if !strings.Contains(screen, "8080   ignore") || strings.Contains(screen, "8080   web") {
		t.Errorf("confirm shows:\n%s", screen)
	}
}
