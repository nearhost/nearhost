package cli

import (
	"charm.land/lipgloss/v2"
	"context"
	"errors"
	"golang.org/x/term"
	"io"
	"os"

	"github.com/nearhost/nearhost/internal/sudo"
	"github.com/nearhost/nearhost/internal/tui"
)

// rootBackend is how the interface reaches sudo; tests use a fake.
type rootBackend struct {
	// needed reports whether steps that need root go through sudo.
	needed func() bool
	// cached reports whether sudo has the user's credentials, without
	// asking.
	cached func(ctx context.Context) bool
	// validate checks a password and caches the credentials; a refused
	// password is tui.ErrWrongPassword.
	validate func(ctx context.Context, password string) error
}

// systemRoot is the production backend.
func systemRoot() rootBackend {
	return rootBackend{
		needed: sudo.Needed,
		cached: sudo.Cached,
		validate: func(ctx context.Context, password string) error {
			return wrongPassword(sudo.Validate(ctx, password))
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
}

// terminalPrompt is how sudo's password prompt is worded in the terminal.
const terminalPrompt = " Password for %p: "

// run runs step, which needs root, for the reason why: at once when sudo has
// the user's credentials, after the password box when it does not, and in
// the terminal when they are not cached even then, under Nearhost's header,
// saying done when it went well. step is given the terminal when it runs
// there, where it may prompt; otherwise nil, and it must not prompt.
func (r asRoot) run(ctx context.Context, why, done string, step func(ctx context.Context, terminal io.Writer) error) error {
	if !r.sudo.needed() || r.sudo.cached(ctx) {
		return step(ctx, nil)
	}
	if err := r.ask(ctx, why, r.sudo.validate); err != nil {
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

// terminalWidth is the width of the terminal on standard output, or 80.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}
