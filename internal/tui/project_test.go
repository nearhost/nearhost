package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// The panel's title links the alias, and the state says how long the
// project has run.
func TestProjectTitleAndUptime(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusConnected
	m := sized(f)
	clock := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	f.setState("myapp", engine.ProjectStatus{State: engine.StateRunning, WebLocalPort: 52114})
	m.Update(tickMsg(clock))
	clock = clock.Add(2*time.Hour + 14*time.Minute)
	m.sel = rowKey(rowProject, "myapp")
	wantScreen(t, m, "● running · up 2h 14m", "80      web         web", "52114     ●", "5173    loopback    vite (VITE_PORT)", "5173      ●")
	if !strings.Contains(m.View().Content, "\x1b]8;;https://myapp.test") {
		t.Error("the alias is not a link")
	}
	f.setState("myapp", engine.ProjectStatus{State: engine.StateStopped})
	m.Update(tickMsg(clock))
	if s := screen(m); strings.Contains(s, "up 2h") {
		t.Errorf("a stopped project still shows its uptime:\n%s", s)
	}
}

func TestFormatUptime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45s", 14 * time.Minute: "14m", 2*time.Hour + 14*time.Minute: "2h 14m", 75 * time.Hour: "3d 3h",
	} {
		if got := formatUptime(d); got != want {
			t.Errorf("formatUptime(%s) = %q, want %q", d, got, want)
		}
	}
}

// Advice is an amber box that says y copies its fix.
func TestAdviceBox(t *testing.T) {
	box := adviceBox(Advisory{Message: "Vite rejects requests for myapp.test unless it is in server.allowedHosts.", Snippet: "allowedHosts"}, 60)
	got := ansi.Strip(strings.Join(box, "\n"))
	want := "╭──────────────────────────────────────────────────────────╮\n" +
		"│ ▲ Vite rejects requests for myapp.test unless it is in   │\n" +
		"│   server.allowedHosts.                        y copy fix │\n" +
		"╰──────────────────────────────────────────────────────────╯"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := ansi.Strip(strings.Join(adviceBox(Advisory{Message: "No fix here."}, 30), "\n")); strings.Contains(got, "copy") {
		t.Errorf("advice with no snippet offers to copy:\n%s", got)
	}
}

// The compact panel puts the state on one line and leaves out the label.
func TestNarrowProjectPanel(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.sel = rowKey(rowProject, "myapp")
	wantScreen(t, m, "myapp  https://myapp.test", "○ stopped · mini · /srv/myapp", " port   role        local   status", "80     web         –       ○")
	if s := screen(m); strings.Contains(s, "vite (VITE_PORT)") {
		t.Errorf("the compact panel shows labels:\n%s", s)
	}
}

// The panel names the presets that apply to the project, once its advice
// has been read, without their registries.
func TestProjectPanelNamesItsPresets(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.edit.presets = map[string][]string{"myapp": {"official/vite", "laravel"}}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	if s := screen(m); strings.Contains(s, "preset") {
		t.Errorf("presets before the advice was read:\n%s", s)
	}
	_, read := m.Update(nil)
	run(m, run(m, read))
	wantScreen(t, m, "server mini    preset vite, laravel    path /srv/myapp")
}
