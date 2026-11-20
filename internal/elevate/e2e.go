//go:build e2e

package elevate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/nearhost/nearhost/internal/sudo"
)

// This file is in builds made with the e2e tag alone, which the journeys in
// internal/e2e make and no release does: TestAReleaseBuildHasNoTestSeam
// there checks a build without the tag. In such a build nothing here runs
// sudo or asks Windows for consent, so a journey can go through the password
// box without touching the machine.

func init() { systemOverride = func() Elevator { return &theE2E } }

var theE2E e2eElevator

// e2eElevator stands in for sudo. The password it takes is the one in
// $NEARHOST_E2E_PASSWORD, and with none set it takes no password at all. A
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

// Run runs c as this user once a password has been taken. Without one it
// fails as sudo -n does: the stand-in has no terminal to ask on.
func (e *e2eElevator) Run(ctx context.Context, c Command) error {
	if !e.Cached(ctx) {
		return errors.New("a password is required")
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
