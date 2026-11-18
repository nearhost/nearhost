package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// An answer meant for a form that has been closed is not taken by the next
// form, even one of the same kind at the same step.
func TestAnswerForAClosedFormIsIgnored(t *testing.T) {
	m, f := serverFormModel(t)
	typeText(m, "first")
	press(m, "enter")
	stale := press(m, "enter") // checking "first"; not run yet
	for i := 0; i < 3; i++ {
		settle(m, press(m, "esc"))
	}
	press(m, "s")
	typeText(m, "second")
	press(m, "enter")
	press(m, "enter") // checking "second"
	settle(m, stale)
	if len(f.forms.addedSrv) != 0 {
		t.Fatalf("the closed form's server was saved: %+v", f.forms.addedSrv)
	}
	wantScreen(t, m, "◐ Connecting to second…")
}

// A connection that arrives after its form closed is closed, not leaked.
func TestConnectionForAClosedFormIsClosed(t *testing.T) {
	m, _, files := projectFormModel(t)
	press(m, "n")
	m.form.(*projectForm).cursor = 1
	connect := press(m, "enter") // connecting to mini; not run yet
	settle(m, press(m, "esc"))
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Fatal("the form is still open")
	}
	settle(m, connect)
	if files.closed != 1 {
		t.Errorf("the late connection was closed %d times", files.closed)
	}
}

// Quitting closes the open form's connection before nearhost stops.
func TestQuitClosesTheFormsConnection(t *testing.T) {
	m, f, files := projectFormModel(t)
	toPath(t, m, 1)
	cmd := press(m, "ctrl+c")
	if !m.quitting {
		t.Fatal("ctrl+c did not quit")
	}
	cmd()
	if files.closed != 1 || len(f.shutdown) != 1 {
		t.Errorf("files closed %d times, shut down %d times", files.closed, len(f.shutdown))
	}
}

// hangOnList makes the form's next listing wait as a read on a hung server
// does, and returns the listing's command running and what it returned.
func hangOnList(t *testing.T, m *Model, files *fakeFiles) <-chan tea.Msg {
	t.Helper()
	files.hang, files.hung = make(chan struct{}), make(chan struct{}, 1)
	list := press(m, "tab")
	listed := make(chan tea.Msg, 1)
	go func() { listed <- list() }()
	select {
	case <-files.hung:
	case <-time.After(2 * time.Second):
		t.Fatal("the listing never began")
	}
	return listed
}

// quitWithin quits and fails the test if quitting takes longer than 2 s.
func quitWithin(t *testing.T, m *Model) {
	t.Helper()
	quit := press(m, "ctrl+c")
	done := make(chan struct{})
	go func() {
		quit()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("quitting waited for a step blocked on the form's connection")
	}
}

// Quitting while a form step waits on a hung server closes the form's
// connection, which ends the wait, so nearhost stops without being killed.
func TestQuitEndsAFormStepBlockedOnItsConnection(t *testing.T) {
	m, f, files := projectFormModel(t)
	toPath(t, m, 1)
	listed := hangOnList(t, m, files)
	quitWithin(t, m)
	select {
	case <-listed:
	default:
		t.Error("the shutdown did not wait for the step its close ended")
	}
	if files.closed != 1 || len(f.shutdown) != 1 {
		t.Errorf("files closed %d times, shut down %d times", files.closed, len(f.shutdown))
	}
}

// A step that closing the connection does not end is waited for only so
// long: quitting goes on without it, and the engine still stops.
func TestQuitDoesNotWaitForeverForAFormStep(t *testing.T) {
	defer func(d time.Duration) { formStopTimeout = d }(formStopTimeout)
	formStopTimeout = 50 * time.Millisecond
	m, f, files := projectFormModel(t)
	toPath(t, m, 1)
	files.keepHung = true
	listed := hangOnList(t, m, files)
	quitWithin(t, m)
	if files.closed != 1 || len(f.shutdown) != 1 {
		t.Errorf("files closed %d times, shut down %d times", files.closed, len(f.shutdown))
	}
	if dl, ok := f.shutdown[0].Deadline(); !ok || time.Until(dl) < shutdownTimeout-time.Second {
		t.Errorf("the engine's shutdown has until %v, %v from now", dl, time.Until(dl))
	}
	close(files.hang)
	<-listed
}

// A form closed before quitting has closed its connection once, and
// quitting does not close it again.
func TestQuitAfterAFormClosedLeavesItsConnection(t *testing.T) {
	m, _, files := projectFormModel(t)
	toPath(t, m, 1)
	for i := 0; i < 3 && m.form != nil; i++ {
		settle(m, press(m, "esc")) // back to the server step, then out
	}
	if m.form != nil || files.closed != 1 {
		t.Fatalf("form %T, files closed %d times", m.form, files.closed)
	}
	quitWithin(t, m)
	if files.closed != 1 {
		t.Errorf("files closed %d times", files.closed)
	}
}

// Quitting ends a server check that is still running and waits for it, so
// its connection is gone before nearhost is.
func TestQuitEndsAndWaitsForARunningCheck(t *testing.T) {
	m, f := serverFormModel(t)
	f.forms.checkBlocks = true
	typeText(m, "box")
	press(m, "enter")
	check := press(m, "enter")
	ended := make(chan error, 1)
	go func() {
		check()
		ended <- nil
	}()
	// Quit once the check is running: a check that has not begun is not
	// waited for, it never starts.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f.mu.Lock()
		running := len(f.forms.checked) > 0
		f.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the check never began")
		}
	}
	quit := press(m, "ctrl+c")
	done := make(chan struct{})
	go func() {
		quit()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("quitting did not end the check")
	}
	select {
	case <-ended:
	default:
		t.Error("the shutdown did not wait for the check")
	}
}

// Text pasted into a field arrives in it, without control characters.
func TestPasteIntoFields(t *testing.T) {
	m, _ := serverFormModel(t)
	m.Update(tea.PasteMsg{Content: "dev@box\x1b[2J\n.lan\u202e"})
	wantScreen(t, m, "host      dev@box[2J.lan▏")

	m, _, _ = projectFormModel(t)
	toPath(t, m, 1)
	m.Update(tea.PasteMsg{Content: "code/blog"})
	wantScreen(t, m, "path      ~/code/blog▏")
}

// After a dropped connection, ctrl+r on the path step connects again.
func TestReconnectAfterADroppedConnection(t *testing.T) {
	m, f, files := projectFormModel(t)
	toPath(t, m, 1)
	files.listErr = errors.New("connection lost")
	settle(m, press(m, "tab")) // takes code/ from the listing ahead; listing it fails quietly
	if strings.Contains(screen(m), "Connection lost") {
		t.Fatal("a listing ahead said it failed")
	}
	settle(m, press(m, "tab")) // lists again, and says why it cannot
	wantScreen(t, m, "Connection lost", "ctrl+r reconnect")
	files.listErr = nil
	settle(m, press(m, "ctrl+r"))
	if len(f.forms.opened) != 2 || files.closed != 1 {
		t.Errorf("opened %d times, closed %d times", len(f.forms.opened), files.closed)
	}
	wantScreen(t, m, "path      ~/code/▏blob/")
}
