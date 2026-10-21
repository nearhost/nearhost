package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
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

// Before the first look at the hosts file, the header says it is checking
// rather than warning that the block needs an update.
func TestHeaderWhileCheckingHosts(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.hostsState = "checking"
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	wantScreen(t, m, "CA trusted · hosts checking…", "hosts     ● checking…")
	if s := screen(m); strings.Contains(s, "needs update") {
		t.Errorf("screen warns while checking:\n%s", s)
	}
	if _, ok := hostsWord("checking"); !ok {
		t.Error("hostsWord(checking) is a warning")
	}
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
	if got := ansi.StringWidth(lines[2][:strings.Index(lines[2], "│")]); got != narrowSidebar {
		t.Errorf("the sidebar's border is at %d, want %d:\n%s", got, narrowSidebar, screen(m))
	}
}

func TestHostsWord(t *testing.T) {
	for state, want := range map[string]string{
		"2 names": "ok", "1 name": "ok", "no names configured": "ok",
		"not managed (--no-hosts)": "off", "missing a.test": "needs update", "": "…",
		"checking": "checking…",
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

// Where the terminal's line height stretches its cells, a panel's sections
// follow one another without blank lines between them.
func TestTallCellsDropTheGapsBetweenSections(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.sel = rowKey(rowProject, "myapp")
	lines := strings.Split(screen(m), "\n")
	at := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("no %q:\n%s", sub, screen(m))
		return -1
	}
	if got := at(" port    role") - at("server mini"); got != 2 {
		t.Errorf("the table starts %d lines after the details, want 2", got)
	}
	m.Update(uv.CellSizeEvent{Width: 13, Height: 52})
	lines = strings.Split(screen(m), "\n")
	if got := at(" port    role") - at("server mini"); got != 1 {
		t.Errorf("with tall cells the table starts %d lines after the details, want 1", got)
	}
	// The line above the log's rule is the panel's last, Reading advice…
	// here, not a blank one.
	if above := lines[at("log ─")-1]; strings.TrimSpace(above[strings.Index(above, "│")+len("│"):]) == "" {
		t.Errorf("with tall cells a blank line comes before the log:\n%s", screen(m))
	}
}

// The too-small message wraps in windows narrower than itself.
func TestTooSmallMessageFits(t *testing.T) {
	m := New(newFake(t, sampleConfig()), "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	for i, l := range strings.Split(screen(m), "\n") {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("line %d is %d cells wide: %q", i, w, l)
		}
	}
}
