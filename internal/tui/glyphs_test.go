package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// noColour tells the model the terminal shows no colour, as NO_COLOR does.
func noColour(m *Model) {
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ASCII})
}

// Each mark kind has its own glyph, one cell wide, so rows keep their
// columns whatever the state.
func TestGlyphsAreDistinctAndOneCellWide(t *testing.T) {
	seen := map[string]markKind{}
	for _, k := range []markKind{markRunning, markError, markAttention, markStopped} {
		g := glyph(k).text
		if w := ansi.StringWidth(g); w != 1 {
			t.Errorf("glyph %q of kind %d is %d cells wide", g, k, w)
		}
		if other, dup := seen[g]; dup {
			t.Errorf("kinds %d and %d share the glyph %q", other, k, g)
		}
		seen[g] = k
	}
}

// Under NO_COLOR a running project and one in error differ in the sidebar
// at 80 columns, and so do pending and stopped; the rows stay as wide.
func TestSidebarStatesDifferWithoutColour(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	f.setState("myapp", engine.ProjectStatus{State: engine.StateError, Message: "boom"})
	f.setState("old", engine.ProjectStatus{State: engine.StateStopped, Pending: &engine.Change{}})
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	noColour(m)
	rows := map[string]string{}
	for _, l := range sideText(m) {
		for _, name := range []string{"website", "myapp", "old"} {
			if strings.Contains(l, name) {
				rows[name] = l
			}
		}
	}
	marks := map[string]string{}
	for name, row := range rows {
		marks[name] = strings.Fields(row)[0]
		if got, want := ansi.StringWidth(row), ansi.StringWidth(rows["website"]); got != want {
			t.Errorf("row of %s is %d cells wide, want %d", name, got, want)
		}
	}
	for name, want := range map[string]string{"website": "●", "myapp": "✕", "old": "!"} {
		if marks[name] != want {
			t.Errorf("%s has mark %q, want %q (row %q)", name, marks[name], want, rows[name])
		}
	}
	if glyph(markStopped).text == marks["old"] || glyph(markStopped).text == marks["website"] || glyph(markStopped).text == marks["myapp"] {
		t.Error("stopped shares a mark with another state")
	}
}

// A server that failed its login shows the error mark, and a connected one
// the running mark.
func TestServerMarks(t *testing.T) {
	if got := serverMark(transport.StatusAuthFailed).text; got != "✕" {
		t.Errorf("auth failed is %q", got)
	}
	if got := serverMark(transport.StatusConnected).text; got != "●" {
		t.Errorf("connected is %q", got)
	}
	if got := serverMark(transport.StatusDisconnected).text; got != "○" {
		t.Errorf("disconnected is %q", got)
	}
}

// The selected log tab is in brackets when colour is off, and plain with
// colour on; the tab line keeps its width.
func TestLogTabBracketsWithoutColour(t *testing.T) {
	m, _ := logModel(t, 3)
	colour := ansi.Strip(m.logTabs())
	if strings.Contains(colour, "[") {
		t.Errorf("tabs with colour are %q", colour)
	}
	noColour(m)
	plain := ansi.Strip(m.logTabs())
	if !strings.HasPrefix(plain, "[all]") {
		t.Errorf("tabs without colour are %q, want the selected tab in brackets", plain)
	}
	if strings.Count(plain, "[") != 1 {
		t.Errorf("tabs without colour are %q, want one bracketed tab", plain)
	}
	if ansi.StringWidth(plain) != ansi.StringWidth(colour) {
		t.Errorf("the tabs are %d cells wide, were %d", ansi.StringWidth(plain), ansi.StringWidth(colour))
	}
}

// A project in error keeps its error mark when a change also waits for
// approval.
func TestErrorMarkWinsOverPending(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	st := engine.ProjectStatus{State: engine.StateError, Message: "boom", Pending: &engine.Change{}}
	if got := m.projectDot(st); got.text != "✕" || got.style.GetForeground() != colourRed {
		t.Errorf("errored project with a pending change has mark %q", got.text)
	}
	f.setState("myapp", st)
	for _, l := range sideText(m) {
		if strings.Contains(l, "myapp") && !strings.Contains(l, "✕") {
			t.Errorf("sidebar row %q lacks the error mark", l)
		}
	}
}
