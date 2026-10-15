package tui

import (
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// routesModel is a model with myapp (on mini: web 80, loopback 5173,
// ignore 3306) selected and its routes focused.
func routesModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	press(m, "tab")
	if !m.routeFocus {
		t.Fatal("tab did not focus the routes")
	}
	return m, f
}

func roleOf(cfg *config.Config, project string, port int) config.Role {
	p, _ := cfg.Project(project)
	for _, r := range p.Routes {
		if r.Port == port {
			return r.Role
		}
	}
	return ""
}

func TestRoutesFocusAndRoles(t *testing.T) {
	m, f := routesModel(t)
	wantScreen(t, m, "▌80      web", " 5173    loopback", "↑↓ route · space loopback/ignore · w web · y copy advice · tab done")
	press(m, "down")
	run(m, press(m, "space")) // 5173: loopback -> ignore
	if got := roleOf(f.cfg, "myapp", 5173); got != config.RoleIgnore {
		t.Errorf("5173 is %s", got)
	}
	m.Update(appliedMsg{}) // the first change is applied
	press(m, "down")
	run(m, press(m, "w")) // 3306 becomes web, 80 ignore
	if roleOf(f.cfg, "myapp", 3306) != config.RoleWeb || roleOf(f.cfg, "myapp", 80) != config.RoleIgnore {
		t.Errorf("routes after w: %+v", f.cfg.Projects[1].Routes)
	}
	press(m, "tab")
	if m.routeFocus {
		t.Error("tab did not give the focus back")
	}
}

func TestRoutesRefusals(t *testing.T) {
	m, f := routesModel(t)
	press(m, "space")                                                                      // 80 is the web route
	wantScreen(t, m, "This is the web route; select another route and press w to make it") // "web" wraps
	if f.edit.edits != 0 {
		t.Error("a refused change was saved")
	}
	press(m, "down")
	f.edit.editErr = errors.New(`loopback port 5173 used by both "myapp" and "other"`)
	run(m, press(m, "w"))
	wantScreen(t, m, `Loopback port 5173 used by both "myapp" and "other"`)

	// On this machine a route is web or ignored.
	f.cfg.Projects[0].Routes = append(f.cfg.Projects[0].Routes, config.Route{Port: 9000, Role: config.RoleIgnore})
	m.cfg = f.cfg
	m.selectRow(rowKey(rowProject, "website"))
	press(m, "tab")
	press(m, "down")
	press(m, "space")
	wantScreen(t, m, "On this machine a route is web or ignored; press w to make it")

	// Choosing another row ends the focus and clears what the panel said.
	press(m, "esc")
	press(m, "down")
	if m.routeFocus || m.panelErr != "" {
		t.Errorf("focus %v, panel %q after moving on", m.routeFocus, m.panelErr)
	}
}

func TestProjectAdvice(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.edit.advice = map[string][]Advisory{"myapp": {
		{Code: "app-url", Message: "APP_URL should be https://myapp.test.", Snippet: "APP_URL=https://myapp.test"},
		{Code: "vite-port", Message: "Set VITE_PORT.", Snippet: "VITE_PORT=5173"},
	}}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	_, read := m.Update(nil)
	wantScreen(t, m, "◐ Reading advice…")
	run(m, run(m, read)) // the wait for the selection to rest, then the read
	wantScreen(t, m, "▲ APP_URL should be https://myapp.test.", "▲ Set VITE_PORT.", "y copy fix")

	// Read once for an entry; read again when the entry changes.
	m.Update(nil)
	if len(f.edit.adviceAsked) != 1 {
		t.Errorf("asked %d times", len(f.edit.adviceAsked))
	}
	cfg := sampleConfig()
	cfg.Projects[1].Alias = "app.test"
	m.cfg = cfg
	if _, again := m.Update(nil); again == nil {
		t.Error("changed entry, advice not read again")
	}

	// y copies every snippet.
	m.cfg = sampleConfig()
	m.advice["myapp"] = advice{of: entryOf(m.cfg.Projects[1]), items: f.edit.advice["myapp"]}
	if cmd := press(m, "y"); cmd == nil {
		t.Fatal("y gave no command")
	} else if got := clipboardOf(cmd); got != "APP_URL=https://myapp.test\n\nVITE_PORT=5173" {
		t.Errorf("copied %q", got)
	}
	wantScreen(t, m, "Copied the advice for myapp.")
}

func TestProjectAdviceThatCannotBeRead(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.edit.adviceErr = errors.New("ssh mini.lan failed")
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	_, read := m.Update(nil)
	run(m, run(m, read))
	wantScreen(t, m, "Advice could not be read: ssh mini.lan failed", "port    role")
	press(m, "y")
	wantScreen(t, m, "myapp has no advice to copy.")
}

// clipboardOf runs the commands in cmd and returns what one of them puts on
// the clipboard. Bubble Tea's message for it is not exported, so it is
// found by its type's name.
func clipboardOf(cmd tea.Cmd) string {
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				if s := clipboardOf(c); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if v := reflect.ValueOf(msg); v.IsValid() && v.Type().Name() == "setClipboardMsg" {
		return v.String()
	}
	return ""
}

// A project removed from the config file by hand while it is shown is
// reported as gone, not edited back into being.
func TestEditOfAProjectThatHasGone(t *testing.T) {
	m, f := routesModel(t)
	// The file changes: myapp and old are gone. The model still shows them.
	gone := cloneForTest(f.cfg)
	gone.Projects = gone.Projects[:1]
	f.cfg = gone
	press(m, "down")
	run(m, press(m, "w"))
	wantScreen(t, m, "Myapp is no longer in the config")
	if _, back := f.cfg.Project("myapp"); back {
		t.Error("the edit brought myapp back")
	}
}
