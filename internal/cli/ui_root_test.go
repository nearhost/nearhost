package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/sudo"
	"github.com/nearhost/nearhost/internal/tui"
)

// fakeRoot stands for sudo and the interface around a step that needs root.
type fakeRoot struct {
	needed    bool
	cached    bool
	sticks    bool  // whether a password the box takes stays cached
	askErr    error // what the password box answers
	asked     []string
	suspended int
	said      bytes.Buffer
}

func (f *fakeRoot) root() asRoot {
	return asRoot{
		width: func() int { return 60 },
		sudo: rootBackend{
			needed: func() bool { return f.needed },
			cached: func(context.Context) bool { return f.cached },
			validate: func(context.Context, string) error {
				return nil
			},
		},
		ask: func(ctx context.Context, why string, check func(context.Context, string) error) error {
			f.asked = append(f.asked, why)
			if f.askErr != nil {
				return f.askErr
			}
			if err := check(ctx, "secret"); err != nil {
				return err
			}
			f.cached = f.sticks
			return nil
		},
		suspend: func(_ context.Context, fn func(io.Reader, io.Writer) error) error {
			f.suspended++
			return fn(strings.NewReader(""), &f.said)
		},
	}
}

// step records how a step was run: in the terminal, where it may prompt, or
// not.
type step struct {
	runs     int
	terminal bool
}

func (s *step) run(_ context.Context, terminal io.Writer) error {
	s.runs++
	s.terminal = terminal != nil
	return nil
}

// With the credentials cached the step runs at once, asking nothing.
func TestAsRootWithCachedCredentials(t *testing.T) {
	f := &fakeRoot{needed: true, cached: true}
	var s step
	if err := f.root().run(context.Background(), "Why.", "Done.", s.run); err != nil {
		t.Fatal(err)
	}
	if s.runs != 1 || s.terminal || len(f.asked) != 0 || f.suspended != 0 {
		t.Errorf("step %+v, asked %v, suspended %d", s, f.asked, f.suspended)
	}
}

// Without them the box asks, then the step runs with them cached.
func TestAsRootAsksInTheBox(t *testing.T) {
	f := &fakeRoot{needed: true, sticks: true}
	var s step
	if err := f.root().run(context.Background(), "Why.", "Done.", s.run); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.asked, "|") != "Why." || s.runs != 1 || s.terminal || f.suspended != 0 {
		t.Errorf("step %+v, asked %v, suspended %d", s, f.asked, f.suspended)
	}
}

// When the credentials do not stay cached, the step runs in the terminal,
// where it may prompt, under Nearhost's header and why, and the terminal
// is told how it went.
func TestAsRootFallsBackToTheTerminal(t *testing.T) {
	f := &fakeRoot{needed: true}
	var s step
	if err := f.root().run(context.Background(), "Why.", "Done.", s.run); err != nil {
		t.Fatal(err)
	}
	if s.runs != 1 || !s.terminal || f.suspended != 1 {
		t.Errorf("step %+v, suspended %d", s, f.suspended)
	}
	said := f.said.String()
	for _, want := range []string{" ∩ nearhost\r\n", " Why.\r\n", " ✓ Done.\r\n", " Back to nearhost…"} {
		if !strings.Contains(said, want) {
			t.Errorf("the terminal lacks %q:\n%q", want, said)
		}
	}
	if strings.Contains(said, "\x1b[") {
		t.Errorf("colours reached a terminal that is not one: %q", said)
	}
}

// A cancelled box runs nothing.
func TestAsRootCancelled(t *testing.T) {
	f := &fakeRoot{needed: true, askErr: tui.ErrPasswordCancelled}
	var s step
	if err := f.root().run(context.Background(), "Why.", "Done.", s.run); !errors.Is(err, tui.ErrPasswordCancelled) {
		t.Errorf("run = %v", err)
	}
	if s.runs != 0 || f.suspended != 0 {
		t.Errorf("step %+v, suspended %d", s, f.suspended)
	}
}

// With no sudo to go through, or as root, the step runs at once.
func TestAsRootWithoutSudo(t *testing.T) {
	f := &fakeRoot{}
	var s step
	if err := f.root().run(context.Background(), "Why.", "Done.", s.run); err != nil || s.runs != 1 || len(f.asked) != 0 {
		t.Errorf("run = %v, step %+v, asked %v", err, s, f.asked)
	}
}

// The validate the box checks with turns sudo's refusal into the box's.
func TestSystemRootTranslatesAWrongPassword(t *testing.T) {
	if err := wrongPassword(sudo.ErrWrongPassword); !errors.Is(err, tui.ErrWrongPassword) {
		t.Errorf("wrongPassword = %v", err)
	}
	other := errors.New("sudo: unable to resolve host")
	if err := wrongPassword(other); err != other {
		t.Errorf("another error became %v", err)
	}
}

// The interface words sudo's prompt as Nearhost's for every sudo it runs,
// the CA's included, which cannot be given a prompt of their own.
func TestTheInterfaceWordsSudosPrompt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_PROMPT", "")
	ran := false
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--config", t.TempDir()+"/config.yaml", "--https-port", "8443", "--http-port", "8080"); err != nil || !ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if got := os.Getenv("SUDO_PROMPT"); got != " Password for %p: " {
		t.Errorf("SUDO_PROMPT = %q", got)
	}
}

// Root steps take turns: one that waited while the box was open for
// another finds the credentials cached and asks nothing; one whose start
// ends while it waits gives up.
func TestAsRootTakesTurns(t *testing.T) {
	f := &fakeRoot{needed: true, sticks: true}
	root := f.root()
	root.turn = make(chan struct{}, 1)
	release := make(chan struct{})
	ask := root.ask
	root.ask = func(ctx context.Context, why string, check func(context.Context, string) error) error {
		<-release // the box stays open
		return ask(ctx, why, check)
	}
	var first, second step
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- root.run(context.Background(), "First.", "Done.", first.run) }()
	time.Sleep(50 * time.Millisecond)
	go func() { secondDone <- root.run(context.Background(), "Second.", "Done.", second.run) }()
	ctx, cancel := context.WithCancel(context.Background())
	thirdDone := make(chan error, 1)
	go func() { thirdDone <- root.run(ctx, "Third.", "Done.", (&step{}).run) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-thirdDone; !errors.Is(err, context.Canceled) {
		t.Errorf("a start that ended while waiting = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.asked, "|") != "First." || first.runs != 1 || second.runs != 1 {
		t.Errorf("asked %v; runs %d, %d", f.asked, first.runs, second.runs)
	}
}
