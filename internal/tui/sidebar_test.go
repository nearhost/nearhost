package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// sideText is the sidebar as drawn, without colours, a line each.
func sideText(m *Model) []string {
	var out []string
	for _, l := range m.sidebar(m.sideWidth(), m.bodyHeight()) {
		out = append(out, ansi.Strip(l))
	}
	return out
}

// The sidebar groups projects under their servers, a blank line between
// groups, with states at the right.
func TestSidebarLayout(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	f.servers["mini"] = transport.StatusAuthFailed
	m := sized(f)
	want := []string{
		"                            ",
		"▌home             1 running ", // Home is selected at the start
		"                            ",
		" ▾ local                  1 ",
		"   ● website        running ",
		"                            ",
		" ▾ mini       ○ auth failed ",
		"   ○ myapp          stopped ",
		"   ○ old            stopped ",
	}
	got := sideText(m)
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d is %q, want %q", i, got[i], w)
		}
	}
}

// The selected row is filled, with a bar at its start and its name in bold.
func TestSidebarSelection(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.sel = rowKey(rowProject, "myapp")
	var line string
	for _, l := range m.sidebar(m.sideWidth(), m.bodyHeight()) {
		if strings.Contains(ansi.Strip(l), "myapp") {
			line = l
		}
	}
	if !strings.HasPrefix(ansi.Strip(line), "▌  ○ myapp") || ansi.StringWidth(line) != wideSidebar {
		t.Errorf("selected row %q", ansi.Strip(line))
	}
	if !strings.Contains(line, "48;2;31;38;31") {
		t.Errorf("the selected row is not filled: %q", line)
	}
}

// A narrow sidebar leaves the states' words out.
func TestNarrowSidebar(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusConnected
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := sideText(m)
	for i, w := range []string{"▌home           0 ", "", " ▾ local        1 ", "   ○ website      ", "", " ▾ mini ●         "} {
		if w != "" && got[i+1] != w {
			t.Errorf("line %d is %q, want %q", i+1, got[i+1], w)
		}
	}
}

// The spinner turns while a project starts, and stops when none is.
func TestSpinnerTurnsWhileStarting(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	if cmd := m.spin(); cmd != nil {
		t.Fatal("the spinner turned with nothing starting")
	}
	f.setState("myapp", engine.ProjectStatus{State: engine.StateStarting})
	cmd := m.spin()
	if cmd == nil {
		t.Fatal("the spinner did not turn")
	}
	first := m.stateDot(engine.StateStarting).text
	m.Update(cmd())
	if m.stateDot(engine.StateStarting).text == first {
		t.Error("the spinner did not move on")
	}
	f.setState("myapp", engine.ProjectStatus{State: engine.StateRunning})
	if _, next := m.Update(spinner.TickMsg{ID: m.spinner.ID()}); next != nil {
		if _, ok := next().(spinner.TickMsg); ok {
			t.Error("the spinner kept turning with nothing starting")
		}
	}
	if m.spinning {
		t.Error("the spinner is still marked as turning")
	}
}

// A click on a blank line of the sidebar selects nothing.
func TestClickOnABlankLine(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.sel = rowKey(rowProject, "website")
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + 5, Button: tea.MouseLeft})
	if m.sel != rowKey(rowProject, "website") {
		t.Errorf("a click on a blank line selected %q", m.sel)
	}
}

// A long name is cut short so the state stays at the right.
func TestSidebarCutsLongNames(t *testing.T) {
	cfg := sampleConfig()
	cfg.Projects[0].Name = "a-project-with-a-very-long-name"
	f := newFake(t, cfg)
	f.setState(cfg.Projects[0].Name, engine.ProjectStatus{State: engine.StateRunning})
	m := sized(f)
	if got := sideText(m)[4]; got != "   ● a-project-wit… running " {
		t.Errorf("row %q", got)
	}
}
