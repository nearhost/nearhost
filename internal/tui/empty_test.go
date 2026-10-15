package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
		if !strings.Contains(lines[at+i], want) {
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
