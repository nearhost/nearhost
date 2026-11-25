package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
)

// setcapCommand is the command a port that needs permission is fixed with.
const setcapCommand = "sudo setcap cap_net_bind_service=+ep '/usr/bin/nearhost'"

// bindRefused is an error from the user's own action, as the engine words
// it.
func bindRefused() *report.Message {
	return report.Errorf(report.PortNeedsPermission, "HTTPS port 443 needs permission to bind.").
		Because("On Linux, a program may use ports below 1024 only when it has been given permission.").
		Step("Give this copy of nearhost permission:", setcapCommand).
		SuggestDoctor()
}

// The alert shows the whole message: the title, why, each step with its
// command on a line of its own, and the doctor line.
func TestAlertShowsMessage(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.showAlert(homeKey, bindRefused())
	wantText(t, m, "HTTPS port 443 needs permission to bind.",
		"On Linux, a program may use ports below 1024 only when it has been given permission.",
		"Give this copy of nearhost permission:", report.DoctorLine)
	wantScreen(t, m, "  "+setcapCommand)
	// Text it quotes is shown inert.
	m.showAlert(homeKey, report.Errorf(report.Unexpected, "evil \x1b[31mred\x1b[0m title"))
	if s := m.View().Content; strings.Contains(s, "\x1b[31mred") {
		t.Errorf("the alert passes an escape through: %q", s)
	}
}

// Enter and Esc close the alert, and the interface carries on.
func TestAlertDismiss(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	for _, key := range []string{"enter", "esc"} {
		m.showAlert(homeKey, bindRefused())
		if m.alertBox == nil {
			t.Fatal("no alert open")
		}
		press(m, "j") // taken by the alert, which waits
		if m.sel != homeKey {
			t.Fatalf("a key went past the alert: selected %q", m.sel)
		}
		press(m, key)
		if m.alertBox != nil {
			t.Fatalf("%s left the alert open", key)
		}
		if strings.Contains(screen(m), "Something went wrong") {
			t.Errorf("%s: the alert is still drawn:\n%s", key, screen(m))
		}
	}
	press(m, "j")
	if m.sel == homeKey {
		t.Error("the interface did not carry on after the alert")
	}
}

// c copies the alert's first command.
func TestAlertCopiesCommand(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.showAlert(homeKey, bindRefused())
	if got := clipboardOf(press(m, "c")); got != setcapCommand {
		t.Errorf("c copied %q, want %q", got, setcapCommand)
	}
	if m.alertBox == nil {
		t.Error("copying closed the alert")
	}
	wantScreen(t, m, "c copy command")
	// An alert without a command offers no copy.
	m.showAlert(homeKey, report.Errorf(report.Unexpected, "Something failed."))
	if cmd := press(m, "c"); cmd != nil && clipboardOf(cmd) != "" {
		t.Errorf("c copied %q from an alert with no command", clipboardOf(cmd))
	}
	if strings.Contains(screen(m), "copy command") {
		t.Errorf("an alert without a command offers to copy one:\n%s", screen(m))
	}
}

// hostile is a message whose text came from outside: ssh's stderr with
// escapes, a file name with a line break, a project name with markup.
func hostile() *report.Message {
	m := report.Errorf(report.PortInUse, "<script>alert(1)</script> \x1b[31mfailed\x1b[0m").
		Because("ssh said \x1b]0;owned\x07 no").
		Step("Remove the file:", "rm '/tmp/a\nb.sock'").
		With("project", "<script>")
	m.Context = "start <b>\x1b[2Jweb"
	return m
}

// Hostile text keeps its words through the message's JSON, the context
// included, and the alert and the panel draw it inert: no escape reaches
// the screen, a line break in a command does not break the line, and
// markup is shown as text.
func TestHostileTextRoundTripsAndRendersInert(t *testing.T) {
	data, err := json.Marshal(hostile())
	if err != nil {
		t.Fatal(err)
	}
	var back report.Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	want := hostile()
	if back.Title != want.Title || back.Context != want.Context || back.Why != want.Why ||
		len(back.Fix) != 1 || back.Fix[0] != want.Fix[0] || back.Data["project"] != "<script>" {
		t.Fatalf("round trip = %+v, want %+v", back, want)
	}
	f := newFake(t, sampleConfig())
	f.setState("website", engine.ProjectStatus{State: engine.StateError, Message: back.Title, Problem: &back})
	m := sized(f)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	check := func(where string) {
		t.Helper()
		s := m.View().Content
		for _, bad := range []string{"\x1b[31m", "\x1b]0;owned", "\x1b[2J"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: %q reached the screen", where, bad)
			}
		}
		if !strings.Contains(screen(m), "<script>alert(1)</script>") || !strings.Contains(screen(m), "rm '/tmp/a b.sock'") {
			t.Errorf("%s: the text is not shown as text:\n%s", where, screen(m))
		}
	}
	m.sel = rowKey(rowProject, "website")
	check("the panel")
	m.showAlert(m.sel, &back)
	check("the alert")
	wantText(t, m, "start <b>web: <script>alert(1)</script> failed")
}
