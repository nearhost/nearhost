package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
)

// With nothing to serve, the window is the first-run screen: the mark in
// half blocks over the name, what to do, and the machine.
func TestFirstRunScreen(t *testing.T) {
	m := sized(newFake(t, &config.Config{}))
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 30 {
		t.Fatalf("drew %d lines", len(lines))
	}
	for i, l := range lines {
		if ansi.StringWidth(l) != 100 {
			t.Errorf("line %d is %d cells wide", i, ansi.StringWidth(l))
		}
	}
	at := -1
	for i, l := range lines {
		if strings.Contains(l, "▄█████▄") {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no mark:\n%s", screen(m))
	}
	for i, want := range []string{" ▄█████▄ ", "██     ██", "██ ▄▄▄ ██", "██ ▀▀▀ ██", "", "nearhost"} {
		if !lineHas(lines[at+i], want) {
			t.Errorf("line %d is %q, want %q", at+i, lines[at+i], want)
		}
	}
	if col := strings.Index(lines[at+5], "nearhost"); col != 46 {
		t.Errorf("the name starts at %d, want it centred at 46", col)
	}
	wantScreen(t, m, "local CA  ● trusted", "proxy     ◐ starting")
	if s := screen(m); strings.Contains(s, "home") || strings.Contains(s, "│") {
		t.Errorf("the first-run screen has a sidebar:\n%s", s)
	}
	if strings.Contains(screen(m), "web UI") {
		t.Error("the web UI is linked before the proxy has started")
	}
	run(m, func() tea.Msg { return startedMsg{} })
	wantScreen(t, m, "proxy     ● https :443 · http :80", "web UI    https://localhost:6327")
	if !strings.Contains(m.View().Content, "\x1b]8;;https://localhost:6327") {
		t.Error("the web UI address is not a link")
	}
}

// With no web port, there is nothing to link to.
func TestFirstRunScreenWithoutAWebPort(t *testing.T) {
	f := newFake(t, &config.Config{})
	f.webPort = 0
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	wantScreen(t, m, "proxy     ● https :443 · http :80")
	if strings.Contains(screen(m), "web UI") {
		t.Errorf("the web UI is linked without a web port:\n%s", screen(m))
	}
}

// Where the terminal says its cells are about twice as tall as usual, the
// first-run screen draws the mark in 2 rows so it keeps its shape.
func TestFirstRunScreenTallCells(t *testing.T) {
	m := sized(newFake(t, &config.Config{}))
	m.Update(uv.CellSizeEvent{Width: 13, Height: 52})
	lines := strings.Split(screen(m), "\n")
	at := -1
	for i, l := range lines {
		if strings.Contains(l, "▄█▀▀▀▀▀█▄") {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no short mark:\n%s", screen(m))
	}
	for i, want := range []string{"██ ▀▀▀ ██", "", "nearhost"} {
		if !lineHas(lines[at+1+i], want) {
			t.Errorf("line %d is %q, want %q", at+1+i, lines[at+1+i], want)
		}
	}
	m.Update(uv.CellSizeEvent{Width: 13, Height: 28})
	if s := screen(m); !strings.Contains(s, "▄█████▄") {
		t.Errorf("usual cells did not bring the full mark back:\n%s", s)
	}
}

// The short mark takes over from cells 8/3 as tall as wide, where the two
// marks are equally far from square; a terminal that does not know its cell
// size keeps the full one.
func TestCellsTall(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want bool
	}{{8, 16, false}, {10, 23, false}, {3, 8, false}, {3, 9, true}, {13, 52, true}, {0, 0, false}, {0, 20, false}} {
		if got := cellsTall(c.w, c.h); got != c.want {
			t.Errorf("cellsTall(%d, %d) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

// lineHas reports whether line holds want, or is blank when want is empty.
func lineHas(line, want string) bool {
	if want == "" {
		return strings.TrimSpace(line) == ""
	}
	return strings.Contains(line, want)
}

// Keys and clicks for rows that are not shown do nothing.
func TestFirstRunScreenIgnoresRowKeys(t *testing.T) {
	cfg := &config.Config{Servers: []config.Server{{Name: "mini", SSHHost: "mini.lan"}}}
	f := newFake(t, cfg)
	m := sized(f)
	for _, k := range []string{"j", "j", "d", "e", "enter"} {
		press(m, k)
	}
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + 3, Button: tea.MouseLeft})
	if m.sel != homeKey || m.form != nil {
		t.Errorf("selected %q, form %T", m.sel, m.form)
	}
	press(m, "n")
	if _, ok := m.form.(*projectForm); !ok {
		t.Errorf("n opened %T", m.form)
	}
}
