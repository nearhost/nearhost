//go:build unix

package elevate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/nearhost/nearhost/internal/sudo"
)

// sudoStopDelay is how long sudo gets to leave after it was asked to
// terminate before it is killed.
const sudoStopDelay = 2 * time.Second

// System is this OS's Elevator.
func System() Elevator { return sudoElevator{} }

// sudoElevator runs privileged steps through sudo.
type sudoElevator struct{}

// Needed reports whether steps go through sudo: they do unless Nearhost runs
// as root, or there is no sudo to run.
func (sudoElevator) Needed() bool {
	if Elevated() {
		return false
	}
	_, err := sudo.Binary()
	return err == nil
}

// TakesPassword is true: Nearhost collects the password for sudo.
func (sudoElevator) TakesPassword() bool { return true }

// Cached reports whether sudo has the user's credentials now.
func (sudoElevator) Cached(ctx context.Context) bool { return sudo.Cached(ctx) }

// Validate checks password with sudo, which caches the credentials.
func (sudoElevator) Validate(ctx context.Context, password string) error {
	return sudo.Validate(ctx, password)
}

// Run runs c through sudo. When ctx ends while sudo runs, which includes sudo
// waiting for a password, sudo is asked to terminate (and killed if it
// stays), and Run returns ctx's error. When ctx has already ended sudo is not
// started. Without Interactive sudo never prompts, and what it and the
// command said is kept for the error rather than shown.
func (sudoElevator) Run(ctx context.Context, c Command) error {
	cmd, err := sudoCommand(ctx, c)
	if err != nil {
		return err
	}
	// Without a prompt the terminal is in use by someone else (the
	// interface): what sudo and the command say is kept for the error.
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if !c.Interactive {
		cmd.Stdout, cmd.Stderr = &said, &said
	}
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if line := lastLine(said.String()); line != "" {
			return fmt.Errorf("%s", line)
		}
		return err
	}
	return nil
}

// lastLine is the last line of what a command said that is not empty: the
// error it ended with.
func lastLine(said string) string {
	lines := strings.Split(strings.TrimSpace(said), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// sudoCommand is the sudo process that runs c.
func sudoCommand(ctx context.Context, c Command) (*exec.Cmd, error) {
	bin, err := sudo.Binary()
	if err != nil {
		return nil, err
	}
	args := []string{"-p", c.Prompt}
	if !c.Interactive {
		args = []string{"-n"}
	}
	cmd := exec.CommandContext(ctx, bin, append(append(args, c.Path), c.Args...)...)
	cmd.Stdin = strings.NewReader(c.Input)
	// Terminate rather than kill: at its password prompt sudo then puts the
	// terminal back the way it was.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = sudoStopDelay
	return cmd, nil
}
