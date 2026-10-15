package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The header carries the mark and the machine's state, with a rule under
// it and over the footer.
func TestHeaderAndRules(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 30 {
		t.Fatalf("drew %d lines in a 30-line window", len(lines))
	}
	if !strings.HasPrefix(lines[0], " ∩ nearhost ") || !strings.HasSuffix(lines[0], "proxy ● :443 · CA trusted · hosts ok ") {
		t.Errorf("header %q", lines[0])
	}
	for _, i := range []int{1, 28} {
		if lines[i] != strings.Repeat("─", 100) {
			t.Errorf("line %d is %q, want a rule", i, lines[i])
		}
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 100 {
			t.Errorf("line %d is %d cells wide: %q", i, w, l)
		}
	}
}

// The header says what is wrong with the machine.
func TestHeaderShowsTrouble(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.trusted = false
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{errBoom} })
	m.firstRunShown = true
	run(m, m.readMachine())
	wantScreen(t, m, "proxy ● down · CA not trusted · hosts ok")
}

// A narrow window gets one dot for the machine and a narrower sidebar.
func TestNarrowHeader(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	run(m, func() tea.Msg { return startedMsg{} })
	lines := strings.Split(screen(m), "\n")
	if !strings.HasSuffix(lines[0], "● proxy · CA · hosts ") {
		t.Errorf("header %q", lines[0])
	}
	if got := strings.Index(lines[2], "│"); got != narrowSidebar {
		t.Errorf("the sidebar's border is at %d, want %d:\n%s", got, narrowSidebar, screen(m))
	}
}

func TestHostsWord(t *testing.T) {
	for state, want := range map[string]string{
		"2 aliases": "ok", "1 alias": "ok", "no aliases configured": "ok",
		"not managed (--no-hosts)": "off", "missing a.test": "needs update", "": "…",
	} {
		if got, _ := hostsWord(state); ansi.Strip(got) != want {
			t.Errorf("hostsWord(%q) = %q, want %q", state, ansi.Strip(got), want)
		}
	}
}

func TestSpreadAndSection(t *testing.T) {
	if got := spread("left", "right", 12); got != "left   right" {
		t.Errorf("spread = %q", got)
	}
	if got := spread("a long left side", "right", 12); got != "a lon… right" {
		t.Errorf("spread cut = %q", got)
	}
	if got := ansi.Strip(section("log", "following", 20)); got != "log ────── following" {
		t.Errorf("section = %q", got)
	}
}
