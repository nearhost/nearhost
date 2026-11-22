package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// askingModel is a model whose backend asks what quitting does.
func askingModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	f := newFake(t, sampleConfig())
	f.quitChoice = QuitAsk
	return sized(f), f
}

// ended runs the quit's command to its end, and fails unless the program
// then ends.
func ended(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("nothing runs the quit")
	}
	if _, final := m.Update(cmd()); final == nil {
		t.Fatal("the program does not end once the quit is done")
	} else if _, ok := final().(tea.QuitMsg); !ok {
		t.Fatal("the program does not end once the quit is done")
	}
}

// keepRunning is the keep_running the fake's config holds: "" when absent.
func keepRunning(f *fakeBackend) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfg.KeepRunning == nil {
		return ""
	}
	if *f.cfg.KeepRunning {
		return "true"
	}
	return "false"
}

func TestQuitAsksWhenTheChoiceIsAsk(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	wantText(t, m, "Stop Nearhost, or leave it running in the background?", "[ Stop ]", "[ Leave running ]",
		"[ ] Don't ask again")
	if m.quitting || len(f.shutdown) != 0 || len(f.left) != 0 {
		t.Errorf("quitting %v, shutdown %d, left %d before an answer", m.quitting, len(f.shutdown), len(f.left))
	}
}

func TestQuitBoxEnterStops(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	wantRow(t, m, "▸ [ Stop ]", "[ Leave running ]")
	ended(t, m, press(m, "enter"))
	if len(f.shutdown) != 1 || len(f.left) != 0 {
		t.Errorf("shutdown %d, left %d", len(f.shutdown), len(f.left))
	}
	if got := keepRunning(f); got != "" {
		t.Errorf("keep_running %q saved without the box ticked", got)
	}
}

func TestQuitBoxLeave(t *testing.T) {
	for _, remember := range []bool{false, true} {
		m, f := askingModel(t)
		press(m, "q")
		press(m, "tab")
		wantRow(t, m, "[ Stop ]", "▸ [ Leave running ]")
		if remember {
			press(m, "space")
			wantText(t, m, "[x] Don't ask again")
		}
		ended(t, m, press(m, "enter"))
		if len(f.left) != 1 || len(f.shutdown) != 0 {
			t.Fatalf("remember %v: left %d, shutdown %d", remember, len(f.left), len(f.shutdown))
		}
		want, edits := "", 0
		if remember {
			want, edits = "true", 1
		}
		if got := keepRunning(f); got != want {
			t.Errorf("remember %v: keep_running %q, want %q", remember, got, want)
		}
		if f.quitEdits[0] != edits {
			t.Errorf("remember %v: %d edits made before leaving, want %d", remember, f.quitEdits[0], edits)
		}
	}
}

func TestQuitBoxRememberStop(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	press(m, "space")
	ended(t, m, press(m, "enter"))
	if len(f.shutdown) != 1 || f.quitEdits[0] != 1 {
		t.Fatalf("shutdown %d, edits before it %v", len(f.shutdown), f.quitEdits)
	}
	if got := keepRunning(f); got != "false" {
		t.Errorf("keep_running %q, want false", got)
	}
}

// The answer that cannot be saved is noted, and the quit goes ahead.
func TestQuitBoxRememberFailureStillQuits(t *testing.T) {
	m, f := askingModel(t)
	f.edit.editErr = errBoom
	press(m, "q")
	press(m, "space")
	cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("nothing runs the quit")
	}
	m.Update(cmd())
	if len(f.shutdown) != 1 {
		t.Fatalf("shutdown %d", len(f.shutdown))
	}
	if log := logText(m, homeKey); !strings.Contains(log, "Could not save the answer to "+f.path+": boom.") {
		t.Errorf("log:\n%s", log)
	}
}

// Esc closes the box and keeps the interface open; arrows move between the
// buttons as tab does.
func TestQuitBoxEscKeepsTheInterface(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	press(m, "right")
	wantRow(t, m, "▸ [ Leave running ]")
	press(m, "left")
	wantRow(t, m, "▸ [ Stop ]")
	press(m, "esc")
	if s := screen(m); strings.Contains(s, "Leave running") {
		t.Errorf("the box is still open:\n%s", s)
	}
	if m.quitting || len(f.shutdown) != 0 || len(f.left) != 0 {
		t.Errorf("quitting %v, shutdown %d, left %d", m.quitting, len(f.shutdown), len(f.left))
	}
	press(m, "q")
	wantRow(t, m, "▸ [ Stop ]") // the box opens afresh
}

// The box's buttons and check box take clicks.
func TestQuitBoxClicks(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	clickText(t, m, "Don't ask again")
	wantText(t, m, "[x] Don't ask again")
	ended(t, m, clickText(t, m, "Leave running"))
	if len(f.left) != 1 || keepRunning(f) != "true" {
		t.Errorf("left %d, keep_running %q", len(f.left), keepRunning(f))
	}
}

// clickText clicks the middle of text on screen, and returns what the click
// does.
func clickText(t *testing.T, m *Model, text string) tea.Cmd {
	t.Helper()
	x, y := find(t, m, text)
	return click(m, x+ansi.StringWidth(text)/2, y)
}

func TestCtrlCAtTheQuitBoxStops(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	press(m, "tab") // Leave running has the focus; Ctrl-C still stops
	cmd := press(m, "ctrl+c")
	wantScreen(t, m, "Stopping…")
	press(m, "ctrl+c") // cuts the shutdown short
	ended(t, m, cmd)
	if len(f.shutdown) != 1 || f.shutdown[0].Err() != context.Canceled || len(f.left) != 0 {
		t.Errorf("shutdown contexts %v, left %d", f.shutdown, len(f.left))
	}
}

// A Ctrl-C, which is also how a hangup or terminate signal arrives, stops
// without asking: there may be nobody to answer.
func TestCtrlCStopsWithoutAsking(t *testing.T) {
	m, f := askingModel(t)
	ended(t, m, press(m, "ctrl+c"))
	if len(f.shutdown) != 1 || len(f.left) != 0 {
		t.Errorf("shutdown %d, left %d", len(f.shutdown), len(f.left))
	}
}

func TestQuitLeaveSkipsTheBox(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		f := newFake(t, sampleConfig())
		f.quitChoice = QuitLeave
		m := sized(f)
		cmd := press(m, key)
		wantScreen(t, m, "Leaving Nearhost running…")
		ended(t, m, cmd)
		if len(f.left) != 1 || len(f.shutdown) != 0 {
			t.Errorf("%s: left %d, shutdown %d", key, len(f.left), len(f.shutdown))
		}
	}
	f := newFake(t, sampleConfig())
	f.quitChoice = QuitStop
	m := sized(f)
	cmd := press(m, "q")
	wantScreen(t, m, "Stopping…")
	ended(t, m, cmd)
	if len(f.shutdown) != 1 || len(f.left) != 0 {
		t.Errorf("stop: shutdown %d, left %d", len(f.shutdown), len(f.left))
	}
}

func TestLostQuits(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.lost = make(chan error, 1)
	m := sized(f)
	f.lost <- errBoom
	_, cmd := m.Update(m.waitLost()())
	if cmd == nil {
		t.Fatal("the lost core did not end the program")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("the lost core did not end the program")
	}
	if m.Err() != errBoom {
		t.Errorf("the program ended with %v", m.Err())
	}
	if len(f.shutdown) != 0 || len(f.left) != 0 {
		t.Errorf("shutdown %d, left %d", len(f.shutdown), len(f.left))
	}
}

// A core lost while quitting is the quit's own doing: stopping the core
// ends its stream. The quit ends the program, with no error.
func TestLostWhileQuittingIsIgnored(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.lost = make(chan error, 1)
	m := sized(f)
	cmd := press(m, "q")
	f.lost <- errBoom
	if _, next := m.Update(m.waitLost()()); next != nil {
		if _, ok := next().(tea.QuitMsg); ok {
			t.Fatal("the lost core ended the program before the quit had")
		}
	}
	ended(t, m, cmd)
	if m.Err() != nil {
		t.Errorf("the program ended with %v", m.Err())
	}
}

// Keys after the core is lost, before the program has ended, do nothing.
func TestQuitAfterLostDoesNothing(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.lost = make(chan error, 1)
	m := sized(f)
	f.lost <- errBoom
	m.Update(m.waitLost()())
	if cmd := press(m, "ctrl+c"); cmd != nil {
		t.Error("ctrl+c after the core was lost did something")
	}
	if len(f.shutdown) != 0 || len(f.left) != 0 {
		t.Errorf("shutdown %d, left %d", len(f.shutdown), len(f.left))
	}
}

// In a window too small to draw the box, q at the box stops, as its
// default does.
func TestQuitBoxInASmallWindowStops(t *testing.T) {
	m, f := askingModel(t)
	press(m, "q")
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	ended(t, m, press(m, "q"))
	if len(f.shutdown) != 1 {
		t.Errorf("shutdown %d", len(f.shutdown))
	}
}
