package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The log is filtered once per update: the keys, the wheel and the frame
// share one pass until the log, the kind or the search changes.
func TestLogViewFiltersOncePerUpdate(t *testing.T) {
	m := goldenModel(t, 100, 30, homeKey)
	runs := func() int { return m.logView.cache.runs }
	m.View()
	if runs() != 1 {
		t.Fatalf("the first frame filtered %d times, want 1", runs())
	}
	for _, k := range []string{"k", "j", "pgup", "g", "G", "w", "w", "v"} {
		press(m, k)
		m.View()
	}
	m.logWheel(tea.Mouse{Button: tea.MouseWheelUp})
	m.View()
	if runs() != 1 {
		t.Errorf("moving about filtered %d times, want 1", runs())
	}
	press(m, "tab")
	m.View()
	if runs() != 2 {
		t.Errorf("a new kind filtered %d times in all, want 2", runs())
	}
	addGolden(m, 2000, 3)
	m.View()
	m.View()
	if runs() != 3 {
		t.Errorf("new lines filtered %d times in all, want 3", runs())
	}
	press(m, "/")
	typeText(m, "page")
	m.View()
	if runs() != 4 {
		t.Errorf("a search typed in filtered %d times in all, want 4 (the keys wait for the frame)", runs())
	}
	press(m, "enter")
	m.View()
	if runs() != 4 {
		t.Errorf("keeping the search filtered again: %d", runs())
	}
	if m.logTabs() == "" || m.logCount(len(m.shown())) == "" || runs() != 4 {
		t.Errorf("the tabs and the count filtered again: %d", runs())
	}
}

// What a key acts on is always the log as it is now: a line that arrives, or
// an entry that drops off the front, between frames moves the cursor's
// entry the way the unfiltered log would.
func TestLogViewActsOnTheCurrentEntries(t *testing.T) {
	m := goldenModel(t, 100, 30, homeKey)
	m.View()
	press(m, "g")
	m.View()
	oldest := m.shown()[0].seq
	// Appended and dropped with no frame in between: the oldest entry is gone.
	addGolden(m, 2000, 5)
	got, want := m.shown(), refShown(m)
	if len(got) != len(want) || got[0].seq != want[0].seq || got[0].seq == oldest {
		t.Fatalf("shown starts at %d, want %d (was %d)", got[0].seq, want[0].seq, oldest)
	}
	// The cursor's entry is gone, so the newest is under it, as it always was.
	press(m, "j")
	if !m.logView.follow {
		t.Error("the cursor did not fall back to the newest entry")
	}
	// A kept entry stays under the cursor when more arrive.
	press(m, "g")
	press(m, "j")
	press(m, "j")
	seq := m.shown()[m.logView.at(m.shown())].seq
	addGolden(m, 2005, 1)
	if now := m.shown()[m.logView.at(m.shown())].seq; now != seq {
		t.Errorf("the cursor moved from entry %d to %d when a line arrived", seq, now)
	}
}

// Enter opens the request under the cursor, even when lines arrived since
// the last frame.
func TestLogViewOpensTheRequestUnderTheCursor(t *testing.T) {
	m := goldenModel(t, 100, 30, homeKey)
	m.View()
	press(m, "k")
	for m.shown()[m.logView.at(m.shown())].req == nil {
		press(m, "k")
	}
	seq := m.shown()[m.logView.at(m.shown())].seq
	addGolden(m, 2000, 40)
	press(m, "enter")
	if m.logView.open != seq {
		t.Errorf("opened entry %d, want %d", m.logView.open, seq)
	}
}

// An entry's height without drawing it is the lines entryLines draws.
func TestEntryHeightIsTheLinesDrawn(t *testing.T) {
	for i := 0; i < 400; i++ {
		e := goldenEntry(i, goldenAt)
		for _, width := range []int{0, 10, 25, 40, 60, 77, 100, 137} {
			if got, want := entryHeight(e, width), len(entryLines(e, width, true)); got != want {
				t.Fatalf("entry %d at width %d is %d lines, want %d", i, width, got, want)
			}
		}
	}
}

// A frame of a long log draws the rows in view, not every entry.
func TestLogViewDrawsOnlyTheRowsInView(t *testing.T) {
	m := goldenModel(t, 120, 40, homeKey)
	m.View()
	press(m, "g")
	m.View()
	allocs := testing.AllocsPerRun(5, func() { m.logScreen() })
	if allocs > 10000 {
		t.Errorf("a frame of 2000 entries makes %.0f allocations", allocs)
	}
}

// The height of an entry follows the width of the window.
func TestLogViewResizeKeepsTheFrame(t *testing.T) {
	m := goldenModel(t, 100, 30, homeKey)
	g := &goldenRun{t: t, m: m}
	g.ref = m.logView.vp
	g.keys("up", "g", "pgdown", "pgdown")
	for _, w := range [][2]int{{60, 20}, {140, 50}, {100, 30}, {45, 12}} {
		m.Update(tea.WindowSizeMsg{Width: w[0], Height: w[1]})
		g.check(fmt.Sprintf("resized to %dx%d", w[0], w[1]))
	}
}
