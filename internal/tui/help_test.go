package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
)

// ? opens the full list of keys in place of the footer, and ? or esc closes
// it.
func TestFullHelp(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	press(m, "?")
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 30 {
		t.Fatalf("drew %d lines in a 30-line window", len(lines))
	}
	help := strings.Join(lines[30-helpRows(m.width):], "\n")
	for _, want := range []string{"move", "project", "server", "nearhost", "j/k    up and down", "enter  start or stop", "r      reconnect", "o      open in browser", "q      quit",
		"move · mouse", "drag   copy text", "2click copy a word", "click  open an address"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
	if m.footerKeyAt(3) != "" {
		t.Error("the full help takes footer clicks")
	}
	press(m, "esc")
	if m.showHelp {
		t.Error("esc left the help open")
	}
	press(m, "?")
	press(m, "?")
	if m.showHelp {
		t.Error("? left the help open")
	}
}

// The footer says whether enter starts or stops the project, and where the
// selection is.
func TestFooterFollowsTheSelection(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.sel = rowKey(rowProject, "website")
	wantScreen(t, m, " enter stop · o open · tab routes · e edit · d remove · l log · / filter · s server · n add · ? more", "website 1/3 ")
	m.sel = homeKey
	wantScreen(t, m, " l log · / filter · s server · n add · ? help · q quit", "home ")
}

// A narrow footer leaves minor keys out, keeps ? and says there is more.
func TestNarrowFooter(t *testing.T) {
	m := New(newFake(t, sampleConfig()), "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.sel = rowKey(rowProject, "myapp")
	wantScreen(t, m, " enter start · o open · e edit · d remove · l log · / filter · ? more", "2/3 ")
	if m.footerKeyAt(m.width-5) != "" {
		t.Error("a click on the place took a key")
	}
}

// No description is cut short, in whatever columns the window has room for.
func TestHelpOverlayNoCutDescriptions(t *testing.T) {
	for _, w := range []int{80, 100, 120} {
		m := New(newFake(t, sampleConfig()), "/work/here")
		m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		press(m, "?")
		out := screen(m)
		if strings.Contains(out, "…") {
			t.Errorf("width %d cut a description:\n%s", w, out)
		}
		for _, g := range helpGroups {
			for _, b := range g.keys {
				if !strings.Contains(out, b.Help().Desc) {
					t.Errorf("width %d lacks %q", w, b.Help().Desc)
				}
			}
		}
	}
}

// The home screen with the help open fits the window, at the documented
// minimum too, where the help takes the body's place.
func TestHomeHelpFits(t *testing.T) {
	for _, sz := range [][2]int{{80, 20}, {80, 24}, {100, 20}} {
		m := New(newFake(t, sampleConfig()), "/work/here")
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		press(m, "?")
		out := screen(m)
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: drew %d lines:\n%s", sz[0], sz[1], len(lines), out)
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Errorf("%dx%d: line %d is %d cells wide", sz[0], sz[1], i, w)
			}
		}
		for _, g := range helpGroups {
			for _, b := range g.keys {
				if !strings.Contains(out, b.Help().Desc) {
					t.Errorf("%dx%d lacks %q", sz[0], sz[1], b.Help().Desc)
				}
			}
		}
		if !strings.Contains(out, "close help") {
			t.Errorf("%dx%d lacks the close-help hint", sz[0], sz[1])
		}
	}
}
