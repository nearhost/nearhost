package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
)

// A form opens in a box in the middle of the window, with its step, a
// progress bar and its keys, over the screen drawn faint.
func TestFormOpensInABox(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	toPath(t, m, 0)
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 36 {
		t.Fatalf("drew %d lines", len(lines))
	}
	top := (36 - boxHeight) / 2
	left := (120 - boxWidth) / 2
	if got := []rune(lines[top])[left]; got != '╭' {
		t.Errorf("the box's corner is %q at %d,%d:\n%s", got, left, top, screen(m))
	}
	head := lines[top+1]
	if !strings.Contains(head, "│  Add project · path") || !strings.Contains(head, "2 of 7  │") {
		t.Errorf("heading %q", head)
	}
	if !strings.Contains(lines[top+2], "━━━━━━━━━━━━━━━━━━━") {
		t.Errorf("no progress bar: %q", lines[top+2])
	}
	if !strings.Contains(lines[top+boxHeight-2], "tab complete · enter next") {
		t.Errorf("no keys at the box's foot: %q", lines[top+boxHeight-2])
	}
	if strings.TrimSpace(lines[35]) != "" {
		t.Errorf("the footer shows %q under a form", lines[35])
	}
	behind := strings.Split(m.View().Content, "\n")[0]
	if !strings.Contains(behind, "38;2;58;65;59") || strings.Contains(behind, "38;2;214;242;107") {
		t.Errorf("the screen behind the form is not faint: %q", behind)
	}
}

// The box fits a window of the smallest size, and opens over the first-run
// screen.
func TestFormBoxInASmallWindow(t *testing.T) {
	m := New(newFake(t, &config.Config{}), "/work/here")
	m.Update(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})
	press(m, "s")
	lines := strings.Split(screen(m), "\n")
	if len(lines) != minHeight {
		t.Fatalf("drew %d lines", len(lines))
	}
	for i, l := range lines {
		if ansi.StringWidth(l) != minWidth {
			t.Errorf("line %d is %d cells wide", i, ansi.StringWidth(l))
		}
	}
	wantScreen(t, m, "Add server · host", "1 of 3", "host      ▏")
}

// A confirm form's question is its box's title.
func TestConfirmBox(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.sel = rowKey(rowProject, "myapp")
	press(m, "d")
	wantScreen(t, m, "│  Remove myapp?", "It stops now and leaves the config.", "y remove · n keep")
	if s := screen(m); strings.Contains(s, "of 7") {
		t.Errorf("a confirm form shows steps:\n%s", s)
	}
}

// A form with more to say than the box holds grows the box, as far as the
// window allows, and never past it.
func TestFormBoxGrows(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	ask := []string{"Remove everything?"}
	for i := 0; i < 40; i++ {
		ask = append(ask, "A line of the question.")
	}
	m.form = &confirmForm{b: m.b, w: m.work, ask: ask}
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 30 {
		t.Fatalf("drew %d lines", len(lines))
	}
	if !strings.Contains(lines[1], "╭") || !strings.Contains(lines[28], "╰") {
		t.Errorf("the box does not fill the window's height:\n%s", screen(m))
	}
}
