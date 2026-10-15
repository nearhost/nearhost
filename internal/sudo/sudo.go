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
)

// Path is the sudo binary. Tests replace it.
var Path = "sudo"

// ErrWrongPassword is the error of a password sudo refused.
var ErrWrongPassword = errors.New("sorry, try again")

// Needed reports whether a command that needs root goes through sudo: it
// does unless Nearhost runs as root, or there is no sudo to run.
func Needed() bool {
	if os.Geteuid() == 0 {
		return false
	}
	_, err := exec.LookPath(Path)
	return err == nil
}

// Cached reports whether sudo has the user's credentials now, so a command
// run through it will not prompt. It never prompts itself.
func Cached(ctx context.Context) bool {
	return exec.CommandContext(ctx, Path, "-n", "-v").Run() == nil
}

// Validate checks password with sudo, which caches the credentials when it
// is right (by default for a few minutes, for this terminal). A password sudo
// refuses is ErrWrongPassword. The password goes to sudo on its standard
// input, never in its arguments.
func Validate(ctx context.Context, password string) error {
	cmd := exec.CommandContext(ctx, Path, "-S", "-v", "-p", "")
	// sudo's messages are translated; in the C locale a refusal reads the
	// same everywhere.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdin = strings.NewReader(password + "\n")
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
