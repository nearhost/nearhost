package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// wait moves the model's clock on by d.
func wait(m *Model, d time.Duration) {
	now := m.now
	m.now = func() time.Time { return now().Add(d) }
}

// stopClock fixes the model's clock at the time it reads now.
func stopClock(m *Model) {
	at := m.now()
	m.now = func() time.Time { return at }
}

func TestPassPromptIgnoresKeysAsItOpens(t *testing.T) {
	m, f, _ := projectFormModel(t)
	stopClock(m)
	settle(m, func() tea.Msg { return passMsg{passReq(m)} })
	wait(m, 100*time.Millisecond)
	for _, k := range []string{"y", "A", "n", "esc"} {
		settle(m, press(m, k))
	}
	if got := f.passthroughCalls(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wait(m, 700*time.Millisecond) // 800 ms after the box opened
	settle(m, press(m, "y"))
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestDocPromptIgnoresKeysAsItOpens(t *testing.T) {
	m, f, _ := projectFormModel(t)
	stopClock(m)
	settle(m, func() tea.Msg { return docMsg{docReq(m)} })
	wait(m, 100*time.Millisecond)
	for _, k := range []string{"y", "A", "n", "esc"} {
		settle(m, press(m, k))
	}
	if got := f.documentCalls(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if _, ok := m.form.(*docForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wait(m, 700*time.Millisecond)
	settle(m, press(m, "y"))
	if got := f.documentCalls(); !slices.Equal(got, []string{"answer d1 1"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestPassPromptLowerAIsNotAlways(t *testing.T) {
	m, f, _ := projectFormModel(t)
	stopClock(m)
	ask(m, passReq(m))
	wait(m, time.Minute)
	settle(m, press(m, "a"))
	if got := f.passthroughCalls(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	settle(m, press(m, "A"))
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 2"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestDocPromptLowerAIsNotAlways(t *testing.T) {
	m, f, _ := projectFormModel(t)
	stopClock(m)
	askDoc(m, docReq(m))
	wait(m, time.Minute)
	settle(m, press(m, "a"))
	if got := f.documentCalls(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if _, ok := m.form.(*docForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	settle(m, press(m, "A"))
	if got := f.documentCalls(); !slices.Equal(got, []string{"answer d1 2"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestRequestBoxShowsItsKeysOnceTheGuardEnds(t *testing.T) {
	m, _, _ := projectFormModel(t)
	stopClock(m)
	settle(m, func() tea.Msg { return passMsg{passReq(m)} })
	wait(m, 100*time.Millisecond)
	if s := screen(m); strings.Contains(s, "y yes") || !strings.Contains(s, "wants to open port 41873") {
		t.Fatalf("at 100 ms:\n%s", s)
	}
	wait(m, 700*time.Millisecond)
	m.Update(askGuardEndedMsg{})
	wantScreen(t, m, "y yes", "A always allow")
}

func TestProposalBoxIgnoresEnterAsItOpens(t *testing.T) {
	m, _, _ := projectFormModel(t)
	stopClock(m)
	settle(m, func() tea.Msg {
		return proposalMsg{Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog", Expires: m.now().Add(time.Minute)}}
	})
	wantScreen(t, m, "Add project · routes")
	wait(m, 100*time.Millisecond)
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · routes")
	wait(m, 700*time.Millisecond)
	settle(m, press(m, "enter"))
	wantScreen(t, m, "name      blog▏")
}

func TestTheNextRequestHasItsOwnGuard(t *testing.T) {
	m, f, _ := projectFormModel(t)
	stopClock(m)
	first, second := passReq(m), passReq(m)
	second.ID, second.Remote = "r2", 5173
	settle(m, func() tea.Msg { return passMsg{first} })
	settle(m, func() tea.Msg { return passMsg{second} })
	wait(m, askGuard)
	settle(m, press(m, "y")) // answers the first, and the second opens
	if pf, ok := m.form.(*passForm); !ok || pf.r.ID != "r2" {
		t.Fatalf("form %T", m.form)
	}
	settle(m, press(m, "y")) // pressed at once
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1"}) {
		t.Fatalf("calls %v", got)
	}
	wait(m, askGuard)
	settle(m, press(m, "y"))
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1", "answer r2 1"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestHelpNamesShiftedAlways(t *testing.T) {
	for _, g := range helpGroups {
		for _, b := range g.keys {
			if b.Help().Key == "A" && b.Help().Desc == "always allow" {
				return
			}
		}
	}
	t.Fatal("the help does not name A for always allow")
}
