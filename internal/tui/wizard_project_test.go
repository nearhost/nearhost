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
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// settle runs cmd and what follows from it, feeding each message to the
// model, until a form says it is done or nothing more follows. The message
// a form ends with is fed to the model, and what the model does next (such
// as applying the config) is not run.
func settle(m *Model, cmd tea.Cmd) {
	for depth := 0; cmd != nil && depth < 20; depth++ {
		msg := cmd()
		switch msg := msg.(type) {
		case nil:
			return
		case tea.BatchMsg:
			for _, c := range msg {
				settle(m, c)
			}
			return
		case formClosedMsg, serverAddedMsg, projectAddedMsg, changedMsg:
			if pf, ok := m.form.(*projectForm); ok && pf.sub != nil {
				_, cmd = m.Update(msg)
				continue // the add-server form inside the project form ended
			}
			m.Update(msg)
			return
		default:
			_, cmd = m.Update(msg)
		}
	}
}

func web(port int, label string) config.Route {
	return config.Route{Port: port, Role: config.RoleWeb, Label: label}
}

func ignored(port int, label string) config.Route {
	return config.Route{Port: port, Role: config.RoleIgnore, Label: label}
}

// projectFormModel is a model with a server's files, on which ~ is
// /home/dev, and the add-project form open.
func projectFormModel(t *testing.T) (*Model, *fakeBackend, *fakeFiles) {
	t.Helper()
	f := newFake(t, sampleConfig())
	files := &fakeFiles{home: "/home/dev", dirs: map[string]bool{
		"/home/dev": true, "/home/dev/code": true, "/home/dev/code/blog": true, "/home/dev/code/blob": true,
		"/home/dev/code/shop": true, "/home/dev/.cache": true, "/work/here": true,
	}, drafts: map[string]Draft{
		"/home/dev/code/blog": {Routes: []config.Route{web(80, "web"), ignored(5173, "vite"), ignored(3306, "db")}},
		"/work/here":          {Routes: []config.Route{ignored(8080, "app (APP_PORT)"), ignored(5173, "vite (VITE_PORT)")}},
	}}
	f.forms.files = files
	m := sized(f)
	return m, f, files
}

// toPath opens the form, picks the server in row, and waits for its files.
func toPath(t *testing.T, m *Model, row int) {
	t.Helper()
	press(m, "n")
	pf := m.form.(*projectForm)
	pf.cursor = row
	settle(m, press(m, "enter"))
	if pf.step != projectStepPath || pf.files == nil {
		t.Fatalf("not on the path step with files: step %d, err %q", pf.step, pf.err)
	}
}

func TestProjectFormListsServers(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.sel = rowKey(rowProject, "myapp")
	press(m, "n")
	wantScreen(t, m, "Add project · server", "  local", "▸ mini", "mini.lan", "  New server…", "enter choose · ↑↓ move · esc cancel")
}

func TestProjectFormCompletesPaths(t *testing.T) {
	m, f, _ := projectFormModel(t)
	toPath(t, m, 1) // mini
	if f.forms.opened[0].Name != "mini" {
		t.Fatalf("opened %+v", f.forms.opened)
	}
	wantScreen(t, m, "path      ~/▏", "tab complete · enter next · ctrl+r reconnect · esc back")
	settle(m, press(m, "tab")) // ~/ holds code and .cache; dot names wait for a dot
	wantScreen(t, m, "path      ~/code/▏")
	typeText(m, "b")
	wantScreen(t, m, "path      ~/code/b▏lob/", "blob  blog")
	press(m, "down")
	settle(m, press(m, "tab"))
	wantScreen(t, m, "path      ~/code/blog/▏")
	typeText(m, "x")
	settle(m, press(m, "tab"))
	wantScreen(t, m, "No directories match")
}

func TestProjectFormAddsAProject(t *testing.T) {
	m, f, files := projectFormModel(t)
	f.forms.askRegistry = true
	files.drafts["/work/here"] = Draft{
		Routes:  []config.Route{ignored(8080, "app (APP_PORT)"), ignored(5173, "vite (VITE_PORT)")},
		Presets: []SuggestedPreset{{Name: "nearhost/vite", Title: "Vite", Repo: "nearhost/presets"}},
	}
	files.drafts["/work/here+presets"] = Draft{
		Routes:     []config.Route{web(8080, "app (APP_PORT)"), config.Route{Port: 5173, Role: config.RoleLoopback, Label: "vite"}},
		Advisories: []Advisory{{Code: "vite-config-port", Message: "Vite reads its port from vite.config.", Snippet: "server: { port: 5173 }"}},
		// The notes shown are those of the last discovery.
		Notes: []string{"registry other (o/p) cannot be read."},
	}
	toPath(t, m, 0) // Local
	wantScreen(t, m, "path      /work/here▏")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · presets", "Trust the official presets (nearhost/presets)", "▸ Yes", "Don't ask again")
	settle(m, press(m, "enter"))
	if len(f.forms.answers) != 1 || f.forms.answers[0] != RegistryYes {
		t.Fatalf("answers %v", f.forms.answers)
	}
	wantScreen(t, m, "Presets that suit this project:", "▸ [x] nearhost/vite  Vite · nearhost/presets", "space tick · enter next")
	settle(m, press(m, "enter"))
	if len(f.forms.enabled) != 1 || f.forms.enabled[0][0] != "nearhost/vite" {
		t.Fatalf("enabled %v", f.forms.enabled)
	}
	wantScreen(t, m, "Add project · routes", "server    local", "path      /work/here", "presets   nearhost/vite",
		"Found 2 ports. Pick what to serve.", "[x] 8080    ‹ web ›       https://here.test  app (APP_PORT)", "[x] 5173    loopback      localhost:5173     vite",
		"Advisory (vite-config-port): Vite reads its port from vite.config.", "server: { port: 5173 }", "registry other (o/p) cannot be read.")
	press(m, "enter")
	wantScreen(t, m, "name      here▏", "alias     https://here.test")
	press(m, "tab")
	press(m, "ctrl+u")
	typeText(m, "site.test")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm", "server    local (this machine)", "path      /work/here", "https://site.test", "8080   web       app (APP_PORT)", "enter add · esc back")
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 {
		t.Fatalf("saved %+v", f.forms.saved)
	}
	p := f.forms.saved[0]
	if p.Name != "here" || p.Alias != "site.test" || p.Path != "/work/here" || p.Server != config.LocalServer || !p.Enabled || len(p.Routes) != 2 {
		t.Errorf("saved %+v", p)
	}
	if m.form != nil || m.sel != rowKey(rowProject, "here") || files.closed != 1 {
		t.Errorf("form %T, selected %q, files closed %d times", m.form, m.sel, files.closed)
	}
}

func TestProjectFormRoutesNeedOneWebRoute(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 0)
	settle(m, press(m, "enter")) // /work/here: nothing is web
	press(m, "enter")
	wantScreen(t, m, "Choose the web route with ← or →")
	press(m, "down")
	press(m, "space") // vite: ignore -> web, as loopback means nothing on this machine
	wantScreen(t, m, "[x] 5173    ‹ web ›")
	press(m, "up")
	press(m, "space") // app: ignore -> web, vite back to ignore
	wantScreen(t, m, "[x] 8080    ‹ web ›", "[ ] 5173    ignore")
	press(m, "space") // the web route stays
	wantScreen(t, m, "A project needs its web route", "[x] 8080    ‹ web ›")
	press(m, "right") // app: web -> ignore; loopback is skipped on this machine
	wantScreen(t, m, "[ ] 8080    ‹ ignore ›")
	press(m, "right")
	press(m, "enter")
	wantScreen(t, m, "Add project · name")
}

func TestProjectFormOffersLoopbackOnAServer(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	press(m, "ctrl+u")
	typeText(m, "~/code/blog")
	settle(m, press(m, "enter"))
	press(m, "down")
	press(m, "space") // vite: ignore -> loopback
	wantScreen(t, m, "[x] 5173    ‹ loopback ›  localhost:5173", "loopback: localhost:5173 on this machine reaches port 5173 on mini,")
	press(m, "left") // vite: loopback -> web, which 80 was
	wantScreen(t, m, "[x] 5173    ‹ web ›", "[ ] 80      ignore")
	press(m, "right")
	wantScreen(t, m, "[x] 5173    ‹ loopback ›")
}

func TestProjectFormAsksForTheWebPortWhenNoneWasFound(t *testing.T) {
	m, f, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{Notes: []string{"Its Compose file is read only when a preset asks for it with compose: ports."}}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	wantScreen(t, m, "No ports were found in this project's files.", "web port  ▏", "Its Compose file is read only")
	typeText(m, "x99999")
	press(m, "enter")
	wantScreen(t, m, "The web port is a number from 1 to 65535")
	press(m, "ctrl+u")
	typeText(m, "3000")
	press(m, "enter")
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	if r := f.forms.saved[0].Routes; len(r) != 1 || r[0].Port != 3000 || r[0].Role != config.RoleWeb {
		t.Errorf("routes %+v", r)
	}
}

func TestProjectFormErrors(t *testing.T) {
	m, f, files := projectFormModel(t)
	f.forms.openErr = errors.New("user@mini.lan: Permission denied (publickey).; run `ssh mini.lan` once in a terminal to fix access, then press r to try again")
	press(m, "n")
	m.form.(*projectForm).cursor = 1
	settle(m, press(m, "enter"))
	wantScreen(t, m, "User@mini.lan: Permission denied (publickey).", "r connect again · esc back")
	f.forms.openErr = nil
	settle(m, press(m, "r"))
	wantScreen(t, m, "path      ~/▏")

	typeText(m, "nowhere")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "/home/dev/nowhere: no such directory", "path      ~/nowhere▏")

	press(m, "ctrl+u")
	typeText(m, "~/code/blog")
	settle(m, press(m, "enter"))
	f.forms.nameErr = errors.New(`a project called "blog" already exists, on local`)
	f.forms.aliasErr = errors.New("alias blog.test is already used by project \"blog\"")
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · name", `A project called "blog" already exists, on local`, "Alias blog.test is already used")

	f.forms.nameErr, f.forms.aliasErr = nil, nil
	settle(m, press(m, "enter"))
	f.forms.saveErr = errors.New("loopback port 5173 is already forwarded")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm", "Loopback port 5173 is already forwarded")
	if files.closed != 0 || m.form == nil {
		t.Error("a failed save closed the form")
	}
}

func TestProjectFormAddsANewServerOnTheWay(t *testing.T) {
	m, f, _ := projectFormModel(t)
	f.forms.hosts = []string{"apollo"}
	press(m, "n")
	pf := m.form.(*projectForm)
	pf.cursor = len(pf.servers)  // New server…
	settle(m, press(m, "enter")) // the hosts are read in the background
	wantScreen(t, m, "Add server", "  apollo")
	press(m, "down")
	press(m, "enter")
	settle(m, press(m, "enter"))
	if m.form != pf || pf.sub != nil {
		t.Fatalf("form %T, sub %v", m.form, pf.sub)
	}
	if pf.step != projectStepPath || pf.srv.Name != "apollo" || len(f.forms.addedSrv) != 1 {
		t.Errorf("step %d on %+v, added %+v", pf.step, pf.srv, f.forms.addedSrv)
	}
	wantScreen(t, m, "path      ~/▏")
}

func TestProjectFormBackAndCancel(t *testing.T) {
	m, _, files := projectFormModel(t)
	toPath(t, m, 1)
	press(m, "ctrl+u")
	typeText(m, "~/code/blog")
	settle(m, press(m, "enter"))
	press(m, "enter")
	settle(m, press(m, "enter"))
	for _, want := range []string{"Add project · name", "Add project · routes", "Add project · path", "Add project · server"} {
		settle(m, press(m, "esc"))
		wantScreen(t, m, want)
	}
	if files.closed != 1 {
		t.Errorf("files closed %d times on leaving the path step", files.closed)
	}
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Error("esc on the first step did not close the form")
	}
}

// An add that finishes while an Apply runs leaves its change to the clock,
// so only one Apply runs at a time.
func TestAddedProjectWaitsForARunningApply(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.applying = true
	if _, cmd := m.Update(projectAddedMsg{name: "blog"}); cmd != nil {
		t.Error("a second Apply began while one was running")
	}
	if !strings.Contains(screen(m), "Added blog.") {
		t.Errorf("no note:\n%s", screen(m))
	}
}

// Text typed or pasted into a field cannot carry escape sequences, control
// or format characters onto the screen.
func TestFieldsDropControlCharacters(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	m.Update(tea.KeyPressMsg{Text: "\x1b[2Jco‮de\x07"})
	s := m.View().Content
	if strings.Contains(s, "\x1b[2J") || strings.ContainsAny(s, "‮\x07") {
		t.Errorf("control characters reached the screen: %q", s)
	}
	wantScreen(t, m, "path      ~/[2Jcode▏")
}

// Completion keeps what was typed before the last slash as it is, completes
// . and .. to directories, and when tab has to list, takes the only match
// or offers several in byte order.
func TestProjectFormCompletionEdgeCases(t *testing.T) {
	m, _, files := projectFormModel(t)
	files.dirs["/home/dev/code/café"] = true
	files.dirs["/home/dev/code/cafè"] = true
	toPath(t, m, 1)
	for _, c := range []struct{ typed, want string }{
		{"~/code/..", "~/code/../▏code/"},
		{"~/code/../code/sh", "~/code/../code/shop/▏"},
		{"~/code/caf", "~/code/caf▏è/"},
	} {
		press(m, "ctrl+u")
		typeText(m, c.typed) // no listing ahead runs, so tab lists
		settle(m, press(m, "tab"))
		if got := pathLine(m); got != "path      "+c.want {
			t.Errorf("%s: got %q, want %q", c.typed, got, "path      "+c.want)
		}
	}
	wantScreen(t, m, "cafè  café")
}

// A step that saves something runs to its end: esc while it runs is not
// taken, so what is saved is what the form shows.
func TestFormSavesRunToTheEnd(t *testing.T) {
	m, f, _ := projectFormModel(t)
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	press(m, "down")
	press(m, "space")
	press(m, "enter")
	settle(m, press(m, "enter"))
	save := press(m, "enter")
	press(m, "esc")
	wantScreen(t, m, "Add project · confirm", "◐ Saving…")
	settle(m, save)
	if len(f.forms.saved) != 1 || m.form != nil {
		t.Errorf("saved %d, form %T", len(f.forms.saved), m.form)
	}

	m, f, _ = projectFormModel(t)
	f.forms.askRegistry = true
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	answer := press(m, "enter")
	press(m, "esc")
	wantScreen(t, m, "◐ Trusting the official presets…")
	settle(m, answer)
	wantScreen(t, m, "Add project · routes")

	sm, sf := serverFormModel(t)
	typeText(sm, "box")
	press(sm, "enter")
	check := press(sm, "enter")
	saved := run(sm, check) // the check passed; saving begins
	press(sm, "esc")
	wantScreen(t, sm, "◐ Saving box…")
	settle(sm, saved)
	if len(sf.forms.addedSrv) != 1 || sm.form != nil {
		t.Errorf("server saved %d, form %T", len(sf.forms.addedSrv), sm.form)
	}
}

// The alias follows the name until it is edited itself.
func TestAliasFollowsTheName(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	press(m, "ctrl+u")
	typeText(m, "~/code/blog")
	settle(m, press(m, "enter"))
	press(m, "enter")
	typeText(m, "2")
	wantScreen(t, m, "name      blog2▏", "alias     https://blog2.test")
	wantText(t, m, "What Nearhost calls the project: in the sidebar, the log and `nearhost up blog2`.",
		"The address your browser opens.", "It follows the name until you change it.")
	press(m, "tab")
	press(m, "backspace")
	press(m, "backspace")
	press(m, "backspace")
	press(m, "backspace")
	press(m, "backspace")
	typeText(m, "site.test")
	press(m, "tab")
	typeText(m, "3")
	wantScreen(t, m, "name      blog23▏", "alias     https://blog2site.test")
	typeText(m, " x") // the name has the focus
	wantText(t, m, "`nearhost up 'blog23 x'`.")
	if strings.Contains(screenText(m), "It follows the name until you change it.") {
		t.Errorf("the alias was typed, but the hint says it follows the name:\n%s", screen(m))
	}
}

// The last step shows advice for the roles and alias chosen.
func TestConfirmShowsAdviceForTheFinalChoices(t *testing.T) {
	m, _, files := projectFormModel(t)
	toPath(t, m, 1)
	press(m, "ctrl+u")
	typeText(m, "~/code/blog")
	settle(m, press(m, "enter"))
	press(m, "enter")
	press(m, "tab")
	press(m, "ctrl+u")
	typeText(m, "site.test")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm", "Advisory (app-url): APP_URL should be https://site.test.")
	if len(files.advised) != 1 || files.advised[0] != "site.test" {
		t.Errorf("advised for %v", files.advised)
	}
}

// Routes that came from .nearhostrc say so, and the hosts they ask for are
// listed on the last look for approval, with where a real domain points.
func TestAddFormShowsNearhostrcRoutesAndApprovalItems(t *testing.T) {
	m, f, files := projectFormModel(t)
	rc := &config.RcSnapshot{SHA256: "abc", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}
	files.drafts["/work/here"] = Draft{
		Routes:     []config.Route{{Port: 8220, Role: config.RoleWeb, Label: "app", Hosts: []string{"app.local.dd.io"}}, ignored(5173, "vite")},
		Nearhostrc: rc,
	}
	f.forms.public = map[string]string{"app.local.dd.io": "93.184.216.34"}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · routes", "app.local.dd.io", "from .nearhostrc", "h hosts")
	if strings.Count(screen(m), "from .nearhostrc") != 1 {
		t.Errorf("a route discovery found is marked as from .nearhostrc:\n%s", screen(m))
	}
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm", "Needs your approval:", "app.local.dd.io on port 8220 (a real domain)",
		"resolves to 93.184.216.34 on the internet", "enter approve and save")
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 || !reflect.DeepEqual(f.forms.saved[0].Nearhostrc, rc) {
		t.Fatalf("saved %+v", f.forms.saved)
	}
	if r := f.forms.saved[0].Routes[0]; r.Port != 8220 || len(r.Hosts) != 1 || r.Hosts[0] != "app.local.dd.io" {
		t.Errorf("saved routes %+v", f.forms.saved[0].Routes)
	}
}

// A .nearhostrc that cannot be read is said on the routes step, and the
// project can be added without it.
func TestRoutesStepShowsANearhostrcThatCannotBeRead(t *testing.T) {
	m, f, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{Routes: []config.Route{web(8080, "app")}, RcError: ".nearhostrc: ${APP_PORT} has no value in .env.local or .env, and no default"}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	wantScreen(t, m, ".nearhostrc: ${APP_PORT} has no value", "[x] 8080")
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "enter add · esc back")
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 || f.forms.saved[0].Nearhostrc != nil {
		t.Errorf("saved %+v", f.forms.saved)
	}
}

func TestRoutesStepEditsHosts(t *testing.T) {
	m, f, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{Routes: []config.Route{web(8080, "app"), ignored(5173, "vite")}}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	pf := m.form.(*projectForm)

	press(m, "down")
	press(m, "h") // vite is not a web route
	wantScreen(t, m, "Only a web route has hosts")
	press(m, "up")
	press(m, "h")
	wantScreen(t, m, "hosts     ▏", "enter set hosts · esc cancel")
	typeText(m, "a.local.x.io  B.local.x.io a.local.x.io")
	press(m, "enter")
	if h := pf.routes[0].Hosts; len(h) != 2 || h[0] != "a.local.x.io" || h[1] != "b.local.x.io" {
		t.Fatalf("hosts %v", h)
	}
	wantScreen(t, m, "‹ web ›       https://here.test +2", "web: your browser opens https://here.test, https://a.local.x.io, https://b.local.x.io, and", "Nearhost sends them to port 8080", "h hosts")

	press(m, "h")
	wantScreen(t, m, "hosts     a.local.x.io b.local.x.io▏")
	press(m, "ctrl+u")
	typeText(m, "*.com")
	press(m, "enter")
	wantScreen(t, m, `Invalid host "*.com"`, "hosts     *.com▏")
	if h := pf.routes[0].Hosts; len(h) != 2 || !pf.editingHosts {
		t.Fatalf("a bad host changed the hosts to %v, or closed the field", h)
	}
	press(m, "esc")
	if pf.editingHosts || pf.step != projectStepRoutes || len(pf.routes[0].Hosts) != 2 {
		t.Fatalf("esc: editing %v, step %d, hosts %v", pf.editingHosts, pf.step, pf.routes[0].Hosts)
	}

	// Hosts typed here are approved by typing them.
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm", "enter add · esc back")
	if strings.Contains(screen(m), "Needs your approval:") {
		t.Errorf("typed hosts are asked about:\n%s", screen(m))
	}
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 || len(f.forms.saved[0].Routes[0].Hosts) != 2 {
		t.Errorf("saved %+v", f.forms.saved)
	}
}

// A main name typed in the alias field is approved by being typed, like a
// host; the same name from a draft is not.
func TestFormTypedAliasNeedsNoApproval(t *testing.T) {
	m, _, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{Routes: []config.Route{web(8220, "app")}}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	press(m, "enter")
	pf := m.form.(*projectForm)
	if pf.step != projectStepDetails {
		t.Fatalf("step %d, want the details step", pf.step)
	}
	// The alias is the draft's: it came from discovery, not from typing.
	pf.alias.value = "app.local.x.io"
	if pf.aliasEdited {
		t.Fatal("the alias counts as typed")
	}
	want := []nearhostrc.Item{{Port: 8220, Host: "app.local.x.io", Why: "a real domain"}}
	if got := pf.needsApproval(); !reflect.DeepEqual(got, want) {
		t.Fatalf("from a draft: needsApproval = %+v", got)
	}

	press(m, "tab")
	press(m, "ctrl+u")
	typeText(m, "app.local.x.io")
	if got := pf.needsApproval(); len(got) != 0 {
		t.Fatalf("typed: needsApproval = %+v", got)
	}
}

// docker_clients is listed for approval as an option, not as a host on a
// port.
func TestFormListsDockerClientsForApproval(t *testing.T) {
	m, f, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{
		Routes:     []config.Route{web(8220, "app")},
		Nearhostrc: &config.RcSnapshot{SHA256: "abc", DockerClients: true, Routes: []config.Route{{Port: 8220, Role: config.RoleWeb}}},
	}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm", "Needs your approval:", "docker_clients (lets local Docker containers reach this project)")
	if strings.Contains(screen(m), "on port 0") {
		t.Errorf("an option is listed as a host:\n%s", screen(m))
	}
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 || !f.forms.saved[0].DockerClients() {
		t.Fatalf("saved %+v", f.forms.saved)
	}
}

// The main name a .nearhostrc gives fills the alias field. It is not taken
// as typed, so a real domain is still asked about; typing the project's name
// does not replace it; and an alias typed is kept when discovery runs again.
func TestFormTakesAliasFromNearhostrc(t *testing.T) {
	m, _, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{
		Name: "here", Alias: "app.local.x.io",
		Routes:     []config.Route{web(8220, "app")},
		Nearhostrc: &config.RcSnapshot{SHA256: "abc", Alias: "app.local.x.io", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb}}},
	}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	press(m, "enter")
	pf := m.form.(*projectForm)
	wantScreen(t, m, "alias     https://app.local.x.io")
	want := []nearhostrc.Item{{Port: 8220, Host: "app.local.x.io", Why: "a real domain"}}
	if got := pf.needsApproval(); !reflect.DeepEqual(got, want) {
		t.Fatalf("needsApproval = %+v", got)
	}
	typeText(m, "2")
	wantScreen(t, m, "name      here2▏", "alias     https://app.local.x.io")
	if strings.Contains(screenText(m), "It follows the name until you change it.") {
		t.Errorf("the alias is the .nearhostrc's, but the hint says it follows the name:\n%s", screen(m))
	}

	press(m, "tab")
	press(m, "ctrl+u")
	typeText(m, "mine.test")
	press(m, "esc")
	press(m, "esc")
	if pf.step != projectStepPath {
		t.Fatalf("step %d, want the path step", pf.step)
	}
	settle(m, press(m, "enter"))
	press(m, "enter")
	wantScreen(t, m, "alias     https://mine.test")

	// With the name kept and the alias no longer counted as typed, the
	// next discovery gives the file's main name again.
	pf.aliasEdited = false
	pf.alias.value = "here2.test"
	press(m, "esc")
	press(m, "esc")
	settle(m, press(m, "enter"))
	press(m, "enter")
	wantScreen(t, m, "alias     https://app.local.x.io")
}

// editForm opens the edit form for a local project at /work/here with the
// saved routes, discovering draft, and returns it after discovery.
func editForm(t *testing.T, saved []config.Route, alias string, draft Draft) *projectForm {
	t.Helper()
	m, f, files := projectFormModel(t)
	f.cfg.Projects = append(f.cfg.Projects, config.Project{
		Name: "here", Server: config.LocalServer, Path: "/work/here", Alias: alias, Enabled: true, Routes: saved,
	})
	files.dirs["/work/here"] = true
	files.drafts["/work/here"] = draft
	m.cfg = f.cfg
	m.sel = rowKey(rowProject, "here")
	settle(m, m.action("e"))
	pf, ok := m.form.(*projectForm)
	if !ok {
		t.Fatalf("no project form: %T", m.form)
	}
	settle(m, press(m, "enter"))
	if pf.step < projectStepRoutes {
		t.Fatalf("step %d, err %q; want discovery done", pf.step, pf.err)
	}
	return pf
}

func hosted(port int, role config.Role, hosts ...string) config.Route {
	return config.Route{Port: port, Role: role, Hosts: hosts}
}

func TestEditAddsInferredHosts(t *testing.T) {
	pf := editForm(t, []config.Route{hosted(80, config.RoleWeb, "manual.x.io")}, "here.test",
		Draft{Routes: []config.Route{hosted(80, config.RoleWeb, "app.x.io")}})
	if got, want := pf.routes[0].Hosts, []string{"manual.x.io", "app.x.io"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	var hosts []string
	for _, it := range pf.needsApproval() {
		hosts = append(hosts, it.Host)
	}
	if want := []string{"app.x.io"}; !reflect.DeepEqual(hosts, want) {
		t.Fatalf("needsApproval hosts = %v, want %v", hosts, want)
	}
}

func TestEditKeepsSavedRoleOverHosts(t *testing.T) {
	pf := editForm(t, []config.Route{hosted(9000, config.RoleIgnore)}, "here.test",
		Draft{Routes: []config.Route{hosted(9000, config.RoleWeb, "files.x.io")}})
	r := pf.routes[0]
	if r.Role != config.RoleIgnore || len(r.Hosts) != 0 {
		t.Fatalf("route = %+v, want ignore with no hosts", r)
	}
}

func TestEditNewHostedPortStaysSecondary(t *testing.T) {
	pf := editForm(t, []config.Route{hosted(8220, config.RoleWeb)}, "here.test",
		Draft{Routes: []config.Route{hosted(6001, config.RoleWeb, "ws.x.io"), hosted(8220, config.RoleWeb)}})
	if len(pf.routes) != 2 || pf.routes[0].Port != 8220 || pf.routes[0].Role != config.RoleWeb {
		t.Fatalf("routes = %+v, want 8220 web first", pf.routes)
	}
	if r := pf.routes[1]; r.Port != 6001 || r.Role != config.RoleWeb || !reflect.DeepEqual(r.Hosts, []string{"ws.x.io"}) {
		t.Fatalf("new route = %+v", r)
	}
}

func TestEditInferredHostNotDuplicated(t *testing.T) {
	pf := editForm(t, []config.Route{hosted(80, config.RoleWeb), hosted(81, config.RoleWeb, "Other.x.io")}, "Alias.x.io",
		Draft{Routes: []config.Route{hosted(80, config.RoleWeb, "other.x.io", "alias.x.io", "new.x.io")}})
	if got, want := pf.routes[0].Hosts, []string{"new.x.io"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
}

// A newly found port is deduped as a saved one is: a host another route
// serves, or the alias, is left off, and a port left without hosts follows
// the rule for new ports without them.
func TestEditNewHostedPortNotDuplicated(t *testing.T) {
	pf := editForm(t, []config.Route{hosted(80, config.RoleWeb), hosted(81, config.RoleWeb, "Other.x.io")}, "Here.test",
		Draft{Routes: []config.Route{
			hosted(80, config.RoleWeb),
			hosted(6001, config.RoleWeb, "here.test"),
			hosted(6002, config.RoleWeb, "other.x.io", "ws.x.io"),
		}})
	want := map[int]config.Route{
		6001: {Port: 6001, Role: config.RoleIgnore},
		6002: hosted(6002, config.RoleWeb, "ws.x.io"),
	}
	for _, r := range pf.routes {
		if w, ok := want[r.Port]; ok {
			if r.Role != w.Role || !reflect.DeepEqual(r.Hosts, w.Hosts) && len(r.Hosts)+len(w.Hosts) > 0 {
				t.Errorf("route %d = %+v, want %+v", r.Port, r, w)
			}
			delete(want, r.Port)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing routes %v in %+v", want, pf.routes)
	}
}

// A newly found port's inferred hosts are asked about on save, as those
// added to a saved web route are.
func TestEditNewHostedPortNeedsApproval(t *testing.T) {
	pf := editForm(t, []config.Route{hosted(8220, config.RoleWeb)}, "here.test",
		Draft{Routes: []config.Route{hosted(8220, config.RoleWeb), hosted(6001, config.RoleWeb, "ws.x.io")}})
	var got []string
	for _, it := range pf.needsApproval() {
		got = append(got, fmt.Sprintf("%s:%d", it.Host, it.Port))
	}
	if want := []string{"ws.x.io:6001"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("needsApproval = %v, want %v", got, want)
	}
}

// A web route with no name yet, before the alias is set, says where it will
// be served instead of naming no address.
func TestRoleLineForAWebRouteWithNoName(t *testing.T) {
	f := &projectForm{srv: config.Server{Name: "mini"}, routes: []config.Route{{Port: 80, Role: config.RoleWeb}}}
	got := ansi.Strip(f.roleLine(""))
	if !strings.Contains(got, "web: Nearhost serves port 80 on mini at the project's alias") || strings.Contains(got, "opens ,") {
		t.Errorf("roleLine = %q", got)
	}
	if got := f.opens(0, ""); got != "–" {
		t.Errorf("opens = %q", got)
	}
}
