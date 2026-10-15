package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// started is a model whose engine has started, with the CA trusted or not.
func started(t *testing.T, trusted bool) (*Model, *fakeBackend) {
	t.Helper()
	f := newFake(t, sampleConfig())
	f.trusted = trusted
	m := sized(f)
	m.Update(startedMsg{})
	m.Update(machineMsg{caTrusted: trusted, hostsState: "2 aliases"})
	return m, f
}

func TestFirstRunIsShownWhenTheCAIsUntrusted(t *testing.T) {
	m, _ := started(t, false)
	wantScreen(t, m, "Welcome to Nearhost", "Two things ask for your password:",
		"• now: trusting the local certificate authority,", "so browsers accept https://<name>.test",
		"• later: adding names to /etc/hosts", "enter trust now · s skip")
	// The main screen's keys wait.
	press(m, "n")
	if _, ok := m.form.(*firstRunForm); !ok {
		t.Errorf("n opened %T over the first-run screen", m.form)
	}
}

func TestFirstRunIsNotShownWhenTheCAIsTrusted(t *testing.T) {
	m, _ := started(t, true)
	if m.form != nil {
		t.Errorf("form %T with the CA trusted", m.form)
	}
}

func TestFirstRunTrusts(t *testing.T) {
	m, f := started(t, false)
	f.edit.trustErr = errors.New("the password was refused")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "The password was refused", "enter try again · s skip")
	f.edit.trustErr = nil
	cmd := press(m, "enter")
	wantScreen(t, m, "◐ Trusting the local certificate authority…")
	_, next := m.Update(cmd())
	_, next = m.Update(next())
	if m.form != nil || f.edit.trusts != 2 || m.sel != homeKey {
		t.Fatalf("form %T, trusts %d, selected %q", m.form, f.edit.trusts, m.sel)
	}
	wantScreen(t, m, "The local CA is now trusted.")
	run(m, next) // Home reads the machine again
	wantScreen(t, m, "local CA  ● trusted")
}

func TestFirstRunSkipsForTheSession(t *testing.T) {
	m, _ := started(t, false)
	settle(m, press(m, "s"))
	if m.form != nil {
		t.Fatalf("form %T after skipping", m.form)
	}
	m.Update(machineMsg{caTrusted: false})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.form != nil {
		t.Error("the first-run screen came back in the same session")
	}
	wantScreen(t, m, "local CA  ● not trusted. Run nearhost trust")
}
