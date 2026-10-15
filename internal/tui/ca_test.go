package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Home says when the local CA expires, in amber in its last month and in
// red once it has.
func TestHomeShowsWhenTheCAExpires(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		expires time.Time
		want    string
		colour  string
	}{
		{time.Date(2036, 10, 13, 0, 0, 0, 0, time.UTC), "local CA  ● trusted · expires 2036-10-13", "38;2;124;132;121"},
		{now.Add(10 * 24 * time.Hour), "local CA  ● trusted · expires 2026-10-25", "38;2;242;184;75"},
		{now.Add(-time.Hour), "local CA  ● expired 2026-10-15", "38;2;242;107;107"},
	} {
		f := newFake(t, sampleConfig())
		f.caExpires = c.expires
		m := sized(f)
		m.now = func() time.Time { return now }
		run(m, func() tea.Msg { return startedMsg{} })
		m.firstRunShown = true
		run(m, m.readMachine())
		wantScreen(t, m, c.want)
		var line string
		for _, l := range strings.Split(m.View().Content, "\n") {
			if strings.Contains(l, "expire") {
				line = l
			}
		}
		if !strings.Contains(line, c.colour) {
			t.Errorf("%q is not in the colour %s: %q", c.want, c.colour, line)
		}
	}
}

// With no CA yet there is no date.
func TestHomeWithoutACA(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	if s := screen(m); strings.Contains(s, "expire") {
		t.Errorf("a date for a CA that is not there:\n%s", s)
	}
}
