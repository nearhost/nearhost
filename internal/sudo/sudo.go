// Package sudo checks and caches the user's credentials with sudo, so that
// commands which need root can run without prompting on a terminal that is
// in use.
package sudo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Candidates are the places sudo is looked for, in order, by absolute path:
// a sudo found through PATH could be a fake that is handed the password.
// Tests replace it.
var Candidates = []string{"/usr/bin/sudo", "/bin/sudo", "/usr/local/bin/sudo", "/run/wrappers/bin/sudo"}

// OwnedByRoot reports whether info describes a file owned by uid 0. Tests
// replace it.
var OwnedByRoot = func(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

// ErrNotFound is Binary's error when no candidate will do.
var ErrNotFound = errors.New("no root-owned sudo in /usr/bin, /bin, /usr/local/bin or /run/wrappers/bin")

// Binary returns the first of Candidates that is an executable regular file
// owned by root and not writable by its group or others; ErrNotFound when
// there is none.
func Binary() (string, error) {
	for _, path := range Candidates {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 || !OwnedByRoot(info) {
			continue
		}
		return path, nil
	}
	return "", ErrNotFound
}

// ErrWrongPassword is the error of a password sudo refused.
var ErrWrongPassword = errors.New("sorry, try again")

// Needed reports whether a command that needs root goes through sudo: it
// does unless Nearhost runs as root, or there is no sudo to run.
func Needed() bool {
	if os.Geteuid() == 0 {
		return false
	}
	_, err := Binary()
	return err == nil
}

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
func Validate(ctx context.Context, password string) error {
	path, err := Binary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, "-S", "-v", "-p", "")
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
