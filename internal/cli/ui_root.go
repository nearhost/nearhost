package cli

import (
	"charm.land/lipgloss/v2"
	"context"
	"errors"
	"golang.org/x/term"
	"io"
	"os"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/sudo"
	"github.com/nearhost/nearhost/internal/tui"
)

// rootBackend is how the interface reaches the elevator (sudo, or the UAC
// prompt on Windows); tests use a fake.
type rootBackend struct {
	// needed reports whether steps that need root go through the elevator.
	needed func() bool
	// takesPassword reports whether Nearhost collects the password itself.
	takesPassword func() bool
	// cached reports whether the elevator has the user's credentials,
	// without asking.
	cached func(ctx context.Context) bool
	// validate checks a password and caches the credentials; a refused
	// password is tui.ErrWrongPassword.
	validate func(ctx context.Context, password string) error
	// forget drops the cached credentials; nil when there is nothing to drop.
	forget func(ctx context.Context)
}

// systemRoot is the production backend, built from the elevator e.
func systemRoot(e elevate.Elevator) rootBackend {
	return rootBackend{
		needed:        e.Needed,
		takesPassword: e.TakesPassword,
		cached:        e.Cached,
		forget:        e.Forget,
		validate: func(ctx context.Context, password string) error {
			return wrongPassword(e.Validate(ctx, password))
		},
	}
}

// wrongPassword turns sudo's refusal of a password into the password box's.
func wrongPassword(err error) error {
	if errors.Is(err, sudo.ErrWrongPassword) {
		return tui.ErrWrongPassword
	}
	return err
}

// asRoot runs steps that need root from the interface.
type asRoot struct {
	sudo rootBackend
	// ask asks for the password in the interface's password box.
	ask func(ctx context.Context, why string, check func(context.Context, string) error) error
	// suspend hands the terminal to a function, the interface suspended.
	suspend func(ctx context.Context, fn func(io.Reader, io.Writer) error) error
	// width is the terminal's width.
	width func() int
	// turn lets one step at a time ask, so a step that waited finds the
	// credentials the one before it cached. nil: no turns (tests).
	turn chan struct{}
}

// terminalPrompt is how sudo's password prompt is worded in the terminal.
const terminalPrompt = " Password for %p: "

// run runs step, which needs root, for the reason why: at once when sudo has
// the user's credentials, after the password box when it does not, and in
// the terminal when they are not cached even then, under Nearhost's header,
// saying done when it went well. An elevator that takes no password (the
// UAC prompt) is run with neither the box nor the terminal, one step at a
// time. step is given the terminal when it runs there, where it may prompt;
// otherwise nil, and it must not prompt.
func (r asRoot) run(ctx context.Context, why, done string, step func(ctx context.Context, terminal io.Writer) error) error {
	if !r.sudo.needed() {
		return step(ctx, nil)
	}
	if r.turn != nil {
		select {
		case r.turn <- struct{}{}:
			defer func() { <-r.turn }()
		case <-ctx.Done():
			return ctx.Err() // the start ended while another step asked
		}
	}
	if !r.sudo.takesPassword() {
		return step(ctx, nil)
	}
	// Checked with the turn held: a step that waited finds the credentials
	// the one before it cached.
	if r.sudo.cached(ctx) {
		return step(ctx, nil)
	}
	check, settled := r.checks()
	err := r.ask(ctx, why, check)
	// The box answers as soon as the user cancels, while the check it
	// cancelled may still be waiting for sudo to leave, and then runs
	// sudo -k. The turn is held until that has finished, so the -k cannot
	// drop the credentials the next step's password caches; but for at
	// most cancelledCheckWait, so a sudo that never leaves cannot block
	// every later prompt. In that rare case the late -k can still drop the
	// next step's credentials, which then asks in the terminal.
	cancelled := settled(cancelledCheckWait)
	if err != nil {
		if r.sudo.forget != nil && !cancelled {
			// The check may have cached the credentials before the user
			// cancelled, or the request ended; whatever the error, the
			// next step must ask again. Forgetting nothing is harmless.
			// A check still running at the answer forgets for itself.
			r.sudo.forget(ctx)
		}
		return err
	}
	if r.sudo.cached(ctx) {
		return step(ctx, nil)
	}
	return r.suspend(ctx, func(_ io.Reader, out io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err // ended while the request waited its turn
		}
		lipgloss.Fprint(out, tui.Asking(why, r.width()))
		err := step(ctx, out)
		lipgloss.Fprint(out, tui.Told(err, done))
		return err
	})
}

// checks wraps the backend's validate for one password box. settled closes
// it to new checks, which then fail at once, waits up to limit for those
// running to return, and reports whether any was running. Such a check
// forgets for itself: a cancelled validate forgets as it ends, and a check
// that cached the credentials after the box answered forgets them here,
// since nobody wants them.
func (r asRoot) checks() (check func(context.Context, string) error, settled func(limit time.Duration) bool) {
	var (
		mu      sync.Mutex
		closed  bool
		active  int // checks running now
		running sync.WaitGroup
	)
	check = func(ctx context.Context, password string) error {
		mu.Lock()
		if closed {
			mu.Unlock()
			return context.Canceled // the box has answered; nobody reads this
		}
		active++
		running.Add(1)
		mu.Unlock()
		defer running.Done()
		err := r.sudo.validate(ctx, password)
		mu.Lock()
		active--
		late := closed
		mu.Unlock()
		if late && err == nil && r.sudo.forget != nil {
			r.sudo.forget(ctx)
		}
		return err
	}
	settled = func(limit time.Duration) bool {
		mu.Lock()
		closed = true
		wasRunning := active > 0
		mu.Unlock()
		ended := make(chan struct{})
		go func() {
			running.Wait()
			close(ended)
		}()
		timer := time.NewTimer(limit)
		defer timer.Stop()
		select {
		case <-ended:
		case <-timer.C:
		}
		return wasRunning
	}
	return check, settled
}

// terminalWidth is the width of the terminal on standard output, or 80.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// cancelledCheckWait bounds how long a step holds the turn for a check its
// box cancelled: under the 5 s that quitting waits. sudo itself takes up to
// 4 s (killed 2 s after the cancel, then a sudo -k given 2 s). Tests
// shorten it.
var cancelledCheckWait = 4 * time.Second
