//go:build e2e && unix

package elevate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/sudo"
)

// This file is in builds made with the e2e tag alone, which the journeys in
// internal/e2e make and no release does: TestAReleaseBuildHasNoTestSeam
// there checks a build without the tag. In such a build nothing here runs
// sudo, so a journey can go through the password box without touching the
// machine. e2e_windows.go is the same for the Windows prompt.

func init() { systemOverride = func() Elevator { return &theE2E } }

var theE2E e2eElevator

// e2eElevator stands in for sudo. The password it accepts is the one in
// $NEARHOST_E2E_PASSWORD; with none set it refuses every password. A
// command it is given runs as this user, not as root, and is written to the
// file $NEARHOST_E2E_ELEVATED names, a line a command.
type e2eElevator struct {
	mu     sync.Mutex
	cached bool
}

func (*e2eElevator) Needed() bool        { return true }
func (*e2eElevator) TakesPassword() bool { return true }

func (e *e2eElevator) Cached(context.Context) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cached
}

func (e *e2eElevator) Validate(_ context.Context, password string) error {
	want := os.Getenv("NEARHOST_E2E_PASSWORD")
	if want == "" || password != want {
		return sudo.ErrWrongPassword
	}
	e.mu.Lock()
	e.cached = true
	e.mu.Unlock()
	return nil
}

func (e *e2eElevator) Forget(context.Context) {
	e.mu.Lock()
	e.cached = false
	e.mu.Unlock()
}

// Run runs c as this user once a password has been taken. Without one, an
// interactive command asks for it on the terminal, as sudo -p does, and any
// other fails as sudo -n does.
func (e *e2eElevator) Run(ctx context.Context, c Command) error {
	if !e.Cached(ctx) {
		if !c.Interactive {
			return errors.New("a password is required")
		}
		if err := e.ask(ctx, c.Prompt); err != nil {
			return err
		}
	}
	if log := os.Getenv("NEARHOST_E2E_ELEVATED"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, werr := fmt.Fprintln(f, strings.Join(append([]string{c.Path}, c.Args...), " "))
		if cerr := f.Close(); werr != nil || cerr != nil {
			return errors.Join(werr, cerr)
		}
	}
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Stdin = strings.NewReader(c.Input)
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &said, &said
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(said.String()))
	}
	return nil
}

// ask reads a password from standard input, prompting on standard error,
// as sudo does: three tries, "Sorry, try again." after a wrong one, and what
// is typed never shown on a terminal.
func (e *e2eElevator) ask(ctx context.Context, prompt string) error {
	in := bufio.NewReader(os.Stdin)
	for try := 0; try < 3; try++ {
		if try > 0 {
			fmt.Fprintln(os.Stderr, "Sorry, try again.")
		}
		fmt.Fprint(os.Stderr, prompt)
		var password string
		if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			password = string(b)
		} else {
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return err
			}
			password = strings.TrimRight(line, "\r\n")
		}
		if e.Validate(ctx, password) == nil {
			return nil
		}
	}
	return errors.New("3 incorrect password attempts")
}
