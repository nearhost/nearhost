package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// askFor opens the password box as the backend would, with check standing
// for sudo, and returns where its answer arrives.
func askFor(m *Model, why string, check func(context.Context, string) error) <-chan error {
	done := make(chan error, 1)
	m.Update(passwordMsg{why: why, check: check, done: done})
	return done
}

// The box asks over the screen, hides what is typed, and gives the
// password to the check; the right one closes it.
func TestPasswordBox(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	var got string
	done := askFor(m, "Nearhost needs your password to add a.test to /etc/hosts.", func(_ context.Context, pw string) error {
		got = pw
		return nil
	})
	typeText(m, "hunter2")
	wantScreen(t, m, "Password needed", "Nearhost needs your password to add a.test to /etc/hosts.", "password  •••••••", "enter continue · esc cancel")
	if s := screen(m); strings.Contains(s, "hunter2") {
		t.Fatalf("the password is on the screen:\n%s", s)
	}
	settle(m, press(m, "enter"))
	if got != "hunter2" {
		t.Errorf("checked %q", got)
	}
	if err := <-done; err != nil {
		t.Errorf("answer %v", err)
	}
	if m.password != nil {
		t.Error("the box stayed open")
	}
}

// A wrong password says so and empties the field; the third gives up.
func TestPasswordBoxWrongPasswords(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	tries := 0
	done := askFor(m, "Why.", func(context.Context, string) error {
		tries++
		return ErrWrongPassword
	})
	for i := 0; i < 2; i++ {
		typeText(m, "nope")
		settle(m, press(m, "enter"))
		wantScreen(t, m, "Sorry, try again.")
		if s := screen(m); strings.Contains(s, "•") {
			t.Errorf("the field was not emptied:\n%s", s)
		}
	}
	typeText(m, "nope")
	settle(m, press(m, "enter"))
	if err := <-done; !errors.Is(err, ErrThreeWrong) || err.Error() != "three wrong passwords; nothing was changed" {
		t.Errorf("answer %v", err)
	}
	if tries != 3 || m.password != nil {
		t.Errorf("tries %d, box %v", tries, m.password)
	}
}

// esc cancels, and so does quitting while the box is open.
func TestPasswordBoxCancels(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	done := askFor(m, "Why.", func(context.Context, string) error { return nil })
	press(m, "esc")
	if err := <-done; !errors.Is(err, ErrPasswordCancelled) {
		t.Errorf("esc: %v", err)
	}
	done = askFor(m, "Why.", func(context.Context, string) error { return nil })
	press(m, "ctrl+c")
	if err := <-done; !errors.Is(err, ErrPasswordCancelled) {
		t.Errorf("quit: %v", err)
	}
	done = askFor(m, "Why.", func(context.Context, string) error { return nil })
	if err := <-done; err == nil {
		t.Error("a request while quitting was taken")
	}
}

// The box opens above a form, which waits under it untouched.
func TestPasswordBoxAboveAForm(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 0)
	done := askFor(m, "Why.", func(context.Context, string) error { return nil })
	typeText(m, "pw")
	if pf := m.form.(*projectForm); pf.path.value != "/work/here" {
		t.Errorf("typing reached the form: %q", pf.path.value)
	}
	wantScreen(t, m, "Password needed")
	settle(m, press(m, "enter"))
	<-done
	wantScreen(t, m, "Add project · path")
}

// Something other than a wrong password ends the box with that error.
func TestPasswordBoxOtherError(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	boom := errors.New("sudo: unable to resolve host")
	done := askFor(m, "Why.", func(context.Context, string) error { return boom })
	typeText(m, "pw")
	settle(m, press(m, "enter"))
	if err := <-done; !errors.Is(err, boom) {
		t.Errorf("answer %v", err)
	}
}

// lockedBuffer collects a program's output, safe to read while it writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// shown waits until the program has drawn text.
func (b *lockedBuffer) shown(t *testing.T, text string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		b.mu.Lock()
		ok := strings.Contains(b.buf.String(), text)
		b.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never drawn", text)
		}
	}
}

// Through a real program: the backend asks with Suspender.Password, the
// password typed reaches its check, and asking again waits for the first
// box to close. A request whose context ends takes its box away.
func TestProgramAsksForAPassword(t *testing.T) {
	f := newFake(t, sampleConfig())
	in, typed := io.Pipe()
	t.Cleanup(func() { in.Close() })
	var out lockedBuffer
	p := tea.NewProgram(New(f, "/"), tea.WithInput(in), tea.WithOutput(&out), tea.WithWindowSize(100, 30), tea.WithoutSignalHandler())
	var s Suspender
	if err := s.Password(context.Background(), "Why.", nil); err != errNotRunning {
		t.Fatalf("Password before Attach = %v", err)
	}
	s.Attach(p)
	go p.Run()
	t.Cleanup(p.Kill)
	typeIn := func(text string) { go io.WriteString(typed, text) }

	checked := make(chan string, 2)
	check := func(_ context.Context, pw string) error {
		checked <- pw
		return nil
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- s.Password(context.Background(), "First reason.", check) }()
	out.shown(t, "First reason.")
	go func() { second <- s.Password(context.Background(), "Second reason.", check) }()
	time.Sleep(100 * time.Millisecond) // the second waits its turn
	typeIn("one\r")
	if err := <-first; err != nil || <-checked != "one" {
		t.Fatalf("first = %v", err)
	}
	out.shown(t, "Second reason.")
	typeIn("two\r")
	if err := <-second; err != nil || <-checked != "two" {
		t.Fatalf("second = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- s.Password(ctx, "Third reason.", check) }()
	out.shown(t, "Third reason.")
	cancel()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("a request whose context ended = %v", err)
	}
	time.Sleep(100 * time.Millisecond) // the box is taken away
	typeIn("three\r")
	select {
	case pw := <-checked:
		t.Errorf("checked %q after the request ended", pw)
	case <-time.After(300 * time.Millisecond):
	}
}

// Clicks wait while the box is open.
func TestPasswordBoxTakesNoClicks(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	askFor(m, "Why.", func(context.Context, string) error { return nil })
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + 4, Button: tea.MouseLeft})
	if m.sel != homeKey {
		t.Errorf("a click selected %q under the box", m.sel)
	}
}
