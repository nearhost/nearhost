// Package sudo checks and caches the user's credentials with sudo, so that
// commands which need root can run without prompting on a terminal that is
// in use.
package sudo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Candidates are the places sudo is looked for, in order, by absolute path:
// a sudo found through PATH could be a fake that is handed the password.
// Tests replace it.
var Candidates = []string{"/usr/bin/sudo", "/bin/sudo", "/usr/local/bin/sudo", "/run/wrappers/bin/sudo"}

// ErrNotFound is Binary's error when no candidate will do.
var ErrNotFound = errors.New("no root-owned sudo in /usr/bin, /bin, /usr/local/bin or /run/wrappers/bin")

// Binary returns the first of Candidates that is a regular file owned by
// root, not writable by its group or others, and that this user may run;
// ErrNotFound when there is none.
func Binary() (string, error) {
	for _, path := range Candidates {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !OwnedByRoot(info) || !runnable(path) {
			continue
		}
		return path, nil
	}
	return "", ErrNotFound
}

// ErrWrongPassword is the error of a password sudo refused.
var ErrWrongPassword = errors.New("sorry, try again")

// Cached reports whether sudo has the user's credentials now, so a command
// run through it will not prompt. It never prompts itself.
func Cached(ctx context.Context) bool {
	path, err := Binary()
	if err != nil {
		return false
	}
	return exec.CommandContext(ctx, path, "-n", "-v").Run() == nil
}

// Validate checks password with sudo, which caches the credentials when it
// is right (by default for a few minutes, for this terminal). A password sudo
// refuses is ErrWrongPassword. The password goes to sudo on its standard
// input, never in its arguments.
//
// When ctx has ended by the time sudo returns, the credentials are forgotten
// again: sudo may have cached them before it was stopped, and the caller no
// longer wants them.
func Validate(ctx context.Context, password string) error {
	defer func() {
		if ctx.Err() != nil {
			Forget(ctx)
		}
	}()
	path, err := Binary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, "-S", "-v", "-p", "")
	// sudo's messages are translated; in the C locale a refusal reads the
	// same everywhere.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdin = strings.NewReader(password + "\n")
	// Terminate rather than kill, so sudo leaves cleanly, and stop waiting
	// for output that a helper of sudo's still holds open.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		said := stderr.String()
		if strings.Contains(said, "incorrect password") || strings.Contains(said, "Sorry, try again") {
			return ErrWrongPassword
		}
		if msg := strings.TrimSpace(said); msg != "" {
			return fmt.Errorf("sudo: %s", firstLine(msg))
		}
		return fmt.Errorf("sudo: %w", err)
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimPrefix(line, "sudo: ")
}

// Forget drops the cached credentials, with sudo -k, which never prompts.
// It runs under a context of its own, so one that has ended does not stop
// it, and gives up after forgetGrace. It takes the timestamp away from sudo
// calls made later; a command already running through sudo is not affected.
func Forget(context.Context) {
	path, err := Binary()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), forgetGrace)
	defer cancel()
	_ = exec.CommandContext(ctx, path, "-k").Run()
}

// forgetGrace is how long Forget lets sudo -k run.
const forgetGrace = 2 * time.Second

// stopDelay is how long sudo gets to leave after it was asked to terminate
// before it is killed, and before a process holding its output is let go.
const stopDelay = 2 * time.Second
