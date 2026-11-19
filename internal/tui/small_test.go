package tui

import (
	"context"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// resize gives the model a window of w by h.
func resize(m *Model, w, h int) { m.Update(tea.WindowSizeMsg{Width: w, Height: h}) }

// A window too small to draw takes no key but q: d then y removes nothing,
// and q quits.
func TestTooSmallWindowOnlyQuits(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	resize(m, 60, 15)
	settle(m, press(m, "d"))
	settle(m, press(m, "y"))
	if m.form != nil || len(f.cfg.Projects) != 3 {
		t.Fatalf("form %T, %d projects", m.form, len(f.cfg.Projects))
	}
	resize(m, 100, 30)
	if m.form != nil {
		t.Fatalf("the remove box opened: %T", m.form)
	}
	resize(m, 60, 15)
	press(m, "q")
	if !m.quitting {
		t.Error("q did not quit")
	}
}

// A request waiting for an answer is not approved from a window too small
// to show it, and is still there after the window grows.
func TestTooSmallWindowDoesNotApproveARequest(t *testing.T) {
	m, f, _ := projectFormModel(t)
	ask(m, passReq(m))
	resize(m, 60, 15)
	for _, k := range []string{"y", "A", "enter"} {
		settle(m, press(m, k))
	}
	if got := f.passthroughCalls(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	resize(m, 100, 30)
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wait(m, askGuard) // the guard starts again as the box is first drawn
	settle(m, press(m, "y"))
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1"}) {
		t.Fatalf("calls %v", got)
	}
}

// Text keyed or pasted while the window is too small goes nowhere, and a
// box that was open stays open with what it held.
func TestTooSmallWindowTypesNothing(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	askFor(m, "Why.", func(context.Context, string) error { return nil })
	typeText(m, "ab")
	resize(m, 60, 15)
	typeText(m, "cd")
	m.Update(tea.PasteMsg{Content: "ef"})
	resize(m, 100, 30)
	if m.password == nil {
		t.Fatal("the box closed")
	}
	if got := m.password.input.Value(); got != "ab" {
		t.Errorf("the box holds %q, want ab", got)
	}
}

// A click or the wheel in a window too small does nothing.
func TestTooSmallWindowIgnoresTheMouse(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	before := m.sel
	x := 1
	resize(m, 60, 15)
	if cmd := click(m, 2, 5); cmd != nil {
		t.Error("a click did something")
	}
	if cmd := click(m, x, 14); cmd != nil {
		t.Error("a click on the footer did something")
	}
	m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	resize(m, 100, 30)
	if m.sel != before || m.form != nil || m.showHelp {
		t.Errorf("selection %q, form %T, help %v", m.sel, m.form, m.showHelp)
	}
}

// A request box that opens while the window is too small keeps its guard
// until the guard has passed since the window grew, however long it waited.
func TestTooSmallRequestGuardRestartsOnGrowing(t *testing.T) {
	for _, wait1 := range []time.Duration{100 * time.Millisecond, 2 * time.Second} {
		m, f, _ := projectFormModel(t)
		stopClock(m)
		resize(m, 60, 15)
		settle(m, func() tea.Msg { return passMsg{passReq(m)} })
		wait(m, wait1)
		resize(m, 100, 30)
		settle(m, press(m, "y"))
		if got := f.passthroughCalls(); len(got) != 0 {
			t.Fatalf("after %v: calls %v", wait1, got)
		}
		if _, ok := m.form.(*passForm); !ok {
			t.Fatalf("after %v: form %T", wait1, m.form)
		}
		wait(m, askGuard)
		settle(m, press(m, "y"))
		if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1"}) {
			t.Fatalf("after %v: calls %v", wait1, got)
		}
	}
}

// The wheel does not scroll the log while the window is too small.
func TestTooSmallWindowDoesNotScroll(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	for i := 0; i < 40; i++ {
		m.logs.note("line")
	}
	resize(m, 60, 15)
	m.Update(tea.MouseWheelMsg{X: 50, Y: 5, Button: tea.MouseWheelUp})
	if m.scroll != 0 {
		t.Errorf("scrolled to %d", m.scroll)
	}
	resize(m, 100, 30)
	m.Update(tea.MouseWheelMsg{X: 90, Y: 15, Button: tea.MouseWheelUp})
	if m.scroll == 0 {
		t.Skip("the wheel does not scroll this screen, so the check above shows nothing")
	}
}
