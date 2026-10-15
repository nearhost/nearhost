package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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
	if err := f.root().run(context.Background(), "Why.", s.run); err != nil {
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
	if err := f.root().run(context.Background(), "Why.", s.run); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.asked, "|") != "Why." || s.runs != 1 || s.terminal || f.suspended != 0 {
		t.Errorf("step %+v, asked %v, suspended %d", s, f.asked, f.suspended)
	}
}

// When the credentials do not stay cached, the step runs in the terminal,
// where it may prompt.
func TestAsRootFallsBackToTheTerminal(t *testing.T) {
	f := &fakeRoot{needed: true}
	var s step
	if err := f.root().run(context.Background(), "Why.", s.run); err != nil {
		t.Fatal(err)
	}
	if s.runs != 1 || !s.terminal || f.suspended != 1 {
		t.Errorf("step %+v, suspended %d", s, f.suspended)
	}
}

// A cancelled box runs nothing.
func TestAsRootCancelled(t *testing.T) {
	f := &fakeRoot{needed: true, askErr: tui.ErrPasswordCancelled}
	var s step
	if err := f.root().run(context.Background(), "Why.", s.run); !errors.Is(err, tui.ErrPasswordCancelled) {
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
	if err := f.root().run(context.Background(), "Why.", s.run); err != nil || s.runs != 1 || len(f.asked) != 0 {
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
