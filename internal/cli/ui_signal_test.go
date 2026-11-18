package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/tui"
)

// signalRun runs bare `nearhost` with its interface in a real program
// without a terminal, and with stop signals the test sends.
type signalRun struct {
	// sigs carries the stop signals the interface listens for.
	sigs chan os.Signal
	// shutdowns counts the shutdowns the interface asked for.
	shutdowns atomic.Int32
	// program receives the program once it is made.
	program chan *tea.Program
	// passwordOpened is closed once the program has taken the message
	// that opens the password box.
	passwordOpened chan struct{}
	// ended receives what the run returned.
	ended chan error
}

// startSignalRun starts bare `nearhost` and returns once its program is
// made. The program is killed when the test ends, should it still run.
func startSignalRun(t *testing.T) (*signalRun, *tea.Program) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the engine's run and CA directories
	r := &signalRun{
		sigs:           make(chan os.Signal, 2),
		program:        make(chan *tea.Program, 1),
		passwordOpened: make(chan struct{}),
		ended:          make(chan error, 1),
	}
	ran := false
	b := fakeUI(t, true, &ran)
	b.stops = func() (<-chan os.Signal, func()) { return r.sigs, func() {} }
	b.up.shutdown = func(eng *engine.Engine, ctx context.Context) error {
		r.shutdowns.Add(1)
		return eng.Shutdown(ctx)
	}
	var opened sync.Once
	b.run = func(m *tui.Model, attach func(*tea.Program)) error {
		p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(100, 30), tea.WithoutSignalHandler(),
			tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
				if msg != nil && reflect.TypeOf(msg).String() == "tui.passwordMsg" {
					opened.Do(func() { close(r.passwordOpened) })
				}
				return msg
			}))
		attach(p)
		r.program <- p
		_, err := p.Run()
		return err
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	go func() {
		_, err := runRoot(t, uiRoot(b), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080")
		r.ended <- err
	}()
	select {
	case p := <-r.program:
		t.Cleanup(func() {
			p.Kill()
			<-r.ended
		})
		return r, p
	case err := <-r.ended:
		t.Fatalf("the interface did not run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the interface did not run")
	}
	return nil, nil
}

// waitQuit fails the test unless the run ends within a few seconds, without
// an error, after asking for one shutdown.
func (r *signalRun) waitQuit(t *testing.T, why string) {
	t.Helper()
	select {
	case err := <-r.ended:
		r.ended <- err // for the cleanup
		if err != nil {
			t.Fatalf("the interface ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the interface did not quit on %s", why)
	}
	if n := r.shutdowns.Load(); n != 1 {
		t.Errorf("the interface asked for %d shutdowns, want 1", n)
	}
}

// A closed terminal sends a hangup. The interface quits as it does on q:
// it shuts the engine down, then ends.
func TestTheInterfaceQuitsOnAHangup(t *testing.T) {
	r, _ := startSignalRun(t)
	r.sigs <- syscall.SIGHUP
	r.waitQuit(t, "a hangup")
}

// The password box takes every key but Ctrl-C. A hangup with it open still
// quits, and answers the box as cancelled.
func TestTheInterfaceQuitsOnAHangupWithThePasswordBoxOpen(t *testing.T) {
	r, p := startSignalRun(t)
	var s tui.Suspender
	s.Attach(p)
	asked := make(chan error, 1)
	go func() {
		asked <- s.Password(context.Background(), "Nearhost needs your password.", func(context.Context, string) error { return nil })
	}()
	select {
	case <-r.passwordOpened:
	case <-time.After(10 * time.Second):
		t.Fatal("the password box did not open")
	}
	r.sigs <- syscall.SIGHUP
	r.waitQuit(t, "a hangup with the password box open")
	select {
	case err := <-asked:
		if !errors.Is(err, tui.ErrPasswordCancelled) {
			t.Errorf("the password box answered %v, want it cancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the password box was not answered")
	}
}
