package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// typeText types s into the model, one key per character.
func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// serverFormModel is a model with the add-server form open, and hosts in
// the SSH config.
func serverFormModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	f := newFake(t, sampleConfig())
	f.forms.hosts = []string{"mini.lan", "apollo", "vps", "app.internal"}
	m := sized(f)
	settle(m, press(m, "s"))
	if _, ok := m.form.(*serverForm); !ok {
		t.Fatalf("s opened %T", m.form)
	}
	return m, f
}

func TestServerFormOffersHostsFromTheSSHConfig(t *testing.T) {
	m, _ := serverFormModel(t)
	// mini.lan is configured already, so it is not offered.
	wantScreen(t, m, "Add server", "host      ▏", "  apollo", "  vps", "  app.internal", "enter next · ↑↓ choose · esc cancel")
	if strings.Contains(screen(m), "mini.lan") {
		t.Error("a configured host was offered")
	}
	typeText(m, "ap")
	if s := screen(m); strings.Contains(s, "vps") || !strings.Contains(s, "apollo") || !strings.Contains(s, "app.internal") {
		t.Errorf("filtering by ap:\n%s", s)
	}
	press(m, "down")
	wantScreen(t, m, "▸ apollo")
	press(m, "enter")
	wantScreen(t, m, "host      apollo", "name      apollo▏", "enter check · esc back")
}

func TestServerFormNamesTheServerAfterItsHost(t *testing.T) {
	m, _ := serverFormModel(t)
	typeText(m, "dev@build.lan:2222")
	press(m, "enter")
	wantScreen(t, m, "name      build.lan▏")
}

func TestServerFormRefusesBadHostsAndNames(t *testing.T) {
	m, _ := serverFormModel(t)
	typeText(m, "-oProxyCommand=x")
	press(m, "enter")
	wantScreen(t, m, "An SSH host cannot be empty, start with - or contain spaces")
	press(m, "ctrl+u")
	typeText(m, "box")
	press(m, "enter")
	for _, name := range []string{"local", "mini", ""} {
		press(m, "ctrl+u")
		typeText(m, name)
		press(m, "enter")
		if _, ok := m.form.(*serverForm); !ok || m.form.(*serverForm).step != serverStepName {
			t.Fatalf("name %q was taken", name)
		}
	}
	wantScreen(t, m, "A server needs a name")
}

func TestServerFormChecksSavesAndSelects(t *testing.T) {
	m, f := serverFormModel(t)
	f.forms.checkErr = errors.New("ssh: connect to host box: Connection refused; run `ssh box` once in a terminal to fix access, then press r to try again")
	typeText(m, "box")
	press(m, "enter")
	cmd := press(m, "enter")
	wantScreen(t, m, "◐ Connecting to box…", "esc back")
	run(m, cmd)
	wantScreen(t, m, "Ssh: connect to host box: Connection refused", "r check again · esc back")
	if len(f.forms.addedSrv) != 0 {
		t.Fatal("a server that failed the check was saved")
	}
	f.forms.checkErr = nil
	run(m, run(m, run(m, press(m, "r")))) // check, save, added
	want := config.Server{Name: "box", SSHHost: "box"}
	if len(f.forms.addedSrv) != 1 || f.forms.addedSrv[0] != want {
		t.Fatalf("saved %+v", f.forms.addedSrv)
	}
	if m.form != nil {
		t.Error("the form stayed open")
	}
	if m.sel != rowKey(rowServer, "box") {
		t.Errorf("selected %q", m.sel)
	}
	wantScreen(t, m, "Added server box.")
}

func TestServerFormBackAndCancel(t *testing.T) {
	m, f := serverFormModel(t)
	typeText(m, "box")
	press(m, "enter")
	cmd := press(m, "enter") // the check begins
	press(m, "esc")          // and is left
	run(m, cmd)
	if len(f.forms.addedSrv) != 0 {
		t.Error("a check that was left saved the server")
	}
	wantScreen(t, m, "name      box▏")
	press(m, "esc")
	wantScreen(t, m, "host      box▏")
	run(m, press(m, "esc"))
	if m.form != nil {
		t.Error("esc on the first step did not close the form")
	}
}

// While a form is open its fields take the keys the main screen uses, except
// ctrl+c.
func TestFormTakesTheKeys(t *testing.T) {
	m, f := serverFormModel(t)
	typeText(m, "qnhs/o")
	wantScreen(t, m, "host      qnhs/o▏")
	if m.quitting || len(f.forms.opened) != 0 || len(f.forms.checked) != 0 {
		t.Error("a shortcut ran while the form was open")
	}
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + 2, Button: tea.MouseLeft})
	if m.sel != homeKey {
		t.Errorf("a click in the sidebar selected %q", m.sel)
	}
	press(m, "ctrl+c")
	if !m.quitting {
		t.Error("ctrl+c did not quit")
	}
}

// The hosts of the SSH config are read in the background, not while the
// interface is drawing.
func TestServerFormReadsHostsInTheBackground(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.forms.hosts = []string{"apollo"}
	m := sized(f)
	load := press(m, "s")
	if load == nil || strings.Contains(screen(m), "apollo") {
		t.Fatal("the hosts were read while opening the form")
	}
	settle(m, load)
	wantScreen(t, m, "  apollo")
}

// A host with any kind of space in it is refused, as nearhost add refuses it.
func TestServerFormRefusesAnySpace(t *testing.T) {
	m, _ := serverFormModel(t)
	typeText(m, "box\u00a0lan")
	press(m, "enter")
	wantScreen(t, m, "An SSH host cannot be empty, start with - or contain spaces")
}
