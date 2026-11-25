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
	side, _ := m.sidebar(m.sideWidth(), m.bodyHeight())
	for _, l := range side {
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
		"▌home             1 running ", // Home is selected at the start, on the main panel's first line
		"                            ",
		" ▾ local                  1 ", // the servers follow each other, as Herdr's groups do
		"   ● website        running ",
		" ▾ mini       ✕ auth failed ",
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
	side, _ := m.sidebar(m.sideWidth(), m.bodyHeight())
	for _, l := range side {
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
	for i, w := range []string{"▌home           0 ", "", " ▾ local        1 ", "   ○ website      ", " ▾ mini ●         "} {
		if w != "" && got[i] != w {
			t.Errorf("line %d is %q, want %q", i, got[i], w)
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
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + 1, Button: tea.MouseLeft}) // the line under home
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
	if got := sideText(m)[3]; got != "   ● a-project-wit… running " {
		t.Errorf("row %q", got)
	}
}

// The passthroughs and documents rows are pinned to the bottom, under a
// blank line, counting what agents opened on every server.
func TestSidebarPinsAgentRows(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	f.passes = append(f.passes, passModelPass(m))
	got := sideText(m)
	n := len(got)
	if got[n-3] != strings.Repeat(" ", wideSidebar) ||
		got[n-2] != " passthroughs           ● 1 " || got[n-1] != " documents             none " {
		t.Errorf("pinned lines %q", got[n-3:])
	}
}

// The list scrolls above the pinned rows, which stay.
func TestSidebarPinnedRowsStayWhileListScrolls(t *testing.T) {
	m := New(newFake(t, manyProjects(30)), "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m.sel = rowKey(rowProject, "p29")
	got := sideText(m)
	if !strings.Contains(strings.Join(got, "\n"), "p29") {
		t.Errorf("the selected project is not drawn: %q", got)
	}
	n := len(got)
	if !strings.HasPrefix(got[n-2], " passthroughs") || !strings.HasPrefix(got[n-1], " documents") {
		t.Errorf("last lines %q", got[n-2:])
	}
}

// A sidebar too short for the pinned rows draws the list alone.
func TestSidebarTooShortForPinnedRows(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.sel = rowKey(rowProject, "old")
	side, _ := m.sidebar(m.sideWidth(), 3)
	joined := ansi.Strip(strings.Join(side, "\n"))
	if !strings.Contains(joined, "old") || strings.Contains(joined, "passthroughs") || strings.Contains(joined, "documents") {
		t.Errorf("sidebar %q", joined)
	}
}

// A narrow sidebar shows the counts alone.
func TestNarrowSidebarPinnedCounts(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	f.passes = append(f.passes, passModelPass(m))
	got := sideText(m)
	n := len(got)
	if !strings.HasPrefix(got[n-2], " passthroughs") || !strings.HasSuffix(got[n-2], " 1 ") ||
		!strings.HasPrefix(got[n-1], " documents") || !strings.HasSuffix(got[n-1], " 0 ") {
		t.Errorf("pinned lines %q", got[n-2:])
	}
}

// A filter hides the pinned rows.
func TestSidebarFilterHidesPinnedRows(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.filter.SetValue("x")
	for _, r := range m.rows() {
		if r.kind == rowPasses || r.kind == rowDocs {
			t.Errorf("row %q while filtered", r.key())
		}
	}
	if joined := strings.Join(sideText(m), "\n"); strings.Contains(joined, "passthroughs") || strings.Contains(joined, "documents") {
		t.Errorf("sidebar %q", joined)
	}
}

// down moves from the last project into the pinned rows and stops at the
// last.
func TestMoveIntoPinnedRows(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.sel = rowKey(rowProject, "old")
	for _, want := range []string{"passthroughs", "documents", "documents"} {
		m.move(1)
		if m.sel != want {
			t.Errorf("selected %q, want %q", m.sel, want)
		}
	}
}

// A click on a pinned row selects it.
func TestClickPinnedRow(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	y := m.bodyHeight() - 1
	if got := m.sidebarKeyAt(y); got != "documents" {
		t.Fatalf("line %d maps to %q", y, got)
	}
	if got := m.sidebarKeyAt(y - 2); got != "" {
		t.Errorf("the blank line maps to %q", got)
	}
	click(m, 3, bodyTop+y)
	if m.sel != "documents" {
		t.Errorf("selected %q", m.sel)
	}
}
