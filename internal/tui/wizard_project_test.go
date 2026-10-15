package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
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
	settle(m, press(m, "tab"))
	wantScreen(t, m, "path      ~/code/blo▏", "blob  blog")
	typeText(m, "g")
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
		"Found 2 ports. Pick what to serve.", "[x] 8080    ‹ web ›       app (APP_PORT)", "[x] 5173    loopback      vite",
		"Advisory (vite-config-port): Vite reads its port from vite.config.", "server: { port: 5173 }", "registry other (o/p) cannot be read.")
	press(m, "enter")
	wantScreen(t, m, "name      here▏", "alias     here.test")
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
	wantScreen(t, m, "[x] 5173    ‹ loopback ›", "loopback ports open on 127.0.0.1")
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
// . and .. to directories, and never splits a character.
func TestProjectFormCompletionEdgeCases(t *testing.T) {
	m, _, files := projectFormModel(t)
	files.dirs["/home/dev/code/café"] = true
	files.dirs["/home/dev/code/cafè"] = true
	toPath(t, m, 1)
	for _, c := range []struct{ typed, want string }{
		{"~/code/..", "~/code/../"},
		{"~/code/../code/sh", "~/code/../code/shop/"},
		{"~/code/caf", "~/code/caf"},
	} {
		press(m, "ctrl+u")
		typeText(m, c.typed)
		settle(m, press(m, "tab"))
		wantScreen(t, m, "path      "+c.want+"▏")
	}
	wantScreen(t, m, "cafè  café") // listed in byte order
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
	wantScreen(t, m, "name      blog2▏", "alias     blog2.test")
	press(m, "tab")
	press(m, "backspace")
	press(m, "backspace")
	press(m, "backspace")
	press(m, "backspace")
	press(m, "backspace")
	typeText(m, "site.test")
	press(m, "tab")
	typeText(m, "3")
	wantScreen(t, m, "name      blog23▏", "alias     blog2site.test")
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
