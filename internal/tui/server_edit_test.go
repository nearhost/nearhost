package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// serverRowModel is a model with the server mini selected.
func serverRowModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowServer, "mini"))
	return m, f
}

func TestReconnectAServer(t *testing.T) {
	m, f := serverRowModel(t)
	wantScreen(t, m, "enter collapse · r reconnect · e edit · d remove")
	run(m, press(m, "r"))
	if len(f.edit.reconnected) != 1 || f.edit.reconnected[0] != "mini" {
		t.Fatalf("reconnected %v", f.edit.reconnected)
	}
	wantScreen(t, m, "Reconnecting mini.")
	f.edit.reconnectErr = errors.New("nothing is connected to mini yet")
	run(m, press(m, "r"))
	wantScreen(t, m, "Nothing is connected to mini yet")
}

func TestEditAServerCarriesItsNewNameToItsProjects(t *testing.T) {
	m, f := serverRowModel(t)
	settle(m, press(m, "e"))
	wantScreen(t, m, "Edit mini", "host      mini.lan▏")
	press(m, "enter")
	wantScreen(t, m, "name      mini▏")
	press(m, "ctrl+u")
	typeText(m, "box")
	settle(m, press(m, "enter"))
	srv, ok := f.cfg.Server("box")
	if !ok || srv.SSHHost != "mini.lan" {
		t.Fatalf("servers %+v", f.cfg.Servers)
	}
	for _, name := range []string{"myapp", "old"} {
		if p, _ := f.cfg.Project(name); p.Server != "box" {
			t.Errorf("%s is on %q", name, p.Server)
		}
	}
	if m.form != nil || m.sel != rowKey(rowServer, "box") {
		t.Errorf("form %T, selected %q", m.form, m.sel)
	}
	wantScreen(t, m, "Saved server box.")
}

// A server keeps its own name without being told it is taken.
func TestEditAServerKeepingItsName(t *testing.T) {
	m, f := serverRowModel(t)
	settle(m, press(m, "e"))
	press(m, "enter")
	settle(m, press(m, "enter"))
	if len(f.forms.checked) != 1 || f.edit.edits != 1 {
		t.Errorf("checked %d, edits %d", len(f.forms.checked), f.edit.edits)
	}
}

func TestRemoveAServerWithItsProjects(t *testing.T) {
	m, f := serverRowModel(t)
	press(m, "d")
	wantScreen(t, m, "Remove mini?", "Its projects go with it and stop now: myapp, old.", "y remove · n keep")
	settle(m, press(m, "y"))
	if _, ok := f.cfg.Server("mini"); ok || len(f.cfg.Projects) != 1 || f.cfg.Projects[0].Name != "website" {
		t.Errorf("after removing: %+v", f.cfg)
	}
	if m.sel != homeKey {
		t.Errorf("selected %q", m.sel)
	}
	wantScreen(t, m, "Removed mini.")
}

func TestLocalHasNoServerActions(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowServer, config.LocalServer))
	if s := screen(m); strings.Contains(s, "r reconnect") {
		t.Errorf("Local offers server actions:\n%s", s)
	}
	for _, k := range []string{"r", "e", "d"} {
		run(m, press(m, k))
	}
	if m.form != nil || len(f.edit.reconnected) != 0 {
		t.Errorf("form %T, reconnected %v", m.form, f.edit.reconnected)
	}
}
