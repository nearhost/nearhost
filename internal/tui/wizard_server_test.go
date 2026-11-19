package tui

import (
	"errors"
	"fmt"
	"regexp"
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
	wantScreen(t, m, "Add server", "host      ▏", "  apollo", "  vps", "  app.internal", "enter next · ↑↓ choose · ←→ move · esc cancel")
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
	wantScreen(t, m, "host      apollo", "name      apollo▏", "enter check · ←→ move · esc back")
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

// manyHostsModel is the add-server form with n hosts in the SSH config, named
// h01, h02 and so on.
func manyHostsModel(t *testing.T, n int) *Model {
	t.Helper()
	f := newFake(t, sampleConfig())
	for i := 1; i <= n; i++ {
		f.forms.hosts = append(f.forms.hosts, fmt.Sprintf("h%02d", i))
	}
	m := sized(f)
	settle(m, press(m, "s"))
	return m
}

// hostBoxHeight is the number of lines the form's body takes.
func hostBoxHeight(m *Model) int {
	return len(m.form.(*serverForm).lines(70))
}

func TestServerFormListScrollsWithTheSelection(t *testing.T) {
	m := manyHostsModel(t, 12)
	if s := screen(m); !strings.Contains(s, "h08") || strings.Contains(s, "h09") || !strings.Contains(s, "↓ 4 more") || hasUpNote(s) {
		t.Fatalf("top of the list:\n%s", s)
	}
	for range 8 {
		press(m, "down")
	}
	wantScreen(t, m, "▸ h08", "↓ 4 more")
	press(m, "down") // row 9: the window moves by one
	s := screen(m)
	for _, want := range []string{"▸ h09", "↑ 1 more", "↓ 3 more", "h02"} {
		if !strings.Contains(s, want) {
			t.Errorf("row 9 lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "h01") {
		t.Errorf("h01 still shown:\n%s", s)
	}
	press(m, "down")
	wantScreen(t, m, "▸ h10", "↑ 2 more", "↓ 2 more")
	press(m, "up")
	press(m, "up")
	wantScreen(t, m, "▸ h08", "↑ 2 more", "↓ 2 more") // the window stays until the selection leaves it
	for range 7 {
		press(m, "up")
	}
	s = screen(m)
	if !strings.Contains(s, "▸ h01") || hasUpNote(s) || !strings.Contains(s, "↓ 4 more") {
		t.Errorf("back at the top:\n%s", s)
	}
}

func TestServerFormListKeepsItsHeight(t *testing.T) {
	m := manyHostsModel(t, 12)
	top := hostBoxHeight(m)
	for range 6 {
		press(m, "down")
	}
	mid := hostBoxHeight(m)
	for range 6 {
		press(m, "down")
	}
	if bottom := hostBoxHeight(m); top != mid || mid != bottom {
		t.Errorf("height changed: top %d, middle %d, bottom %d", top, mid, bottom)
	}
	if !strings.Contains(screen(m), "▸ h12") {
		t.Errorf("last row not selected:\n%s", screen(m))
	}
}

func TestServerFormFilterResetsTheWindow(t *testing.T) {
	m := manyHostsModel(t, 12)
	for range 11 {
		press(m, "down")
	}
	typeText(m, "h0")
	s := screen(m)
	if hasUpNote(s) || !strings.Contains(s, "↓ 1 more") || !strings.Contains(s, "h01") {
		t.Errorf("after filtering to nine hosts:\n%s", s)
	}
	press(m, "ctrl+u")
	typeText(m, "h1")
	s = screen(m)
	if strings.Contains(s, "more") || !strings.Contains(s, "h10") || !strings.Contains(s, "h12") {
		t.Errorf("after filtering to three hosts:\n%s", s)
	}
	press(m, "down")
	press(m, "down")
	press(m, "down")
	wantScreen(t, m, "▸ h12")
}

// hasUpNote reports whether the screen shows a "↑ N more" line (the footer's
// ↑↓ hint does not count).
func hasUpNote(s string) bool { return regexp.MustCompile(`↑ \d+ more`).MatchString(s) }

func TestServerFormListFitsTheSmallestWindow(t *testing.T) {
	m := manyHostsModel(t, 12)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	for range 12 {
		press(m, "down")
	}
	wantScreen(t, m, "host      ", "↑ 4 more", "▸ h12", "enter next")
}

// hyphenHostsModel is the form at 80x20 with 12 hosts that all hold a hyphen.
func hyphenHostsModel(t *testing.T) *Model {
	t.Helper()
	f := newFake(t, sampleConfig())
	for i := 1; i <= 12; i++ {
		f.forms.hosts = append(f.forms.hosts, fmt.Sprintf("h-%02d", i))
	}
	m := sized(f)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	settle(m, press(m, "s"))
	return m
}

func TestServerFormErrorShowsWithTheFieldAndAShorterList(t *testing.T) {
	m := hyphenHostsModel(t)
	typeText(m, "-")
	press(m, "enter")
	wantScreen(t, m, "host      -▏", "An SSH host cannot be empty", "h-01", "↓ 8 more", "enter next")
	press(m, "down")
	for range 6 {
		press(m, "down")
	}
	wantScreen(t, m, "host      -", "An SSH host cannot be empty", "▸ h-07", "↑ 3 more", "↓ 5 more")
	press(m, "backspace") // the host field is back to "-" and the hosts match again
	typeText(m, "0")
	s := screen(m)
	if strings.Contains(s, "An SSH host") || !strings.Contains(s, "h-08") || strings.Contains(s, "h-09") {
		t.Errorf("after an edit the list should show eight rows and no error:\n%s", s)
	}
}
