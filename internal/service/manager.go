package service

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
)

// Runner runs a command and returns its combined output. It is the only way
// a Manager runs anything, so tests substitute a recording fake.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager installs, starts and removes the service in this system's service
// manager: launchd, systemd --user or Task Scheduler.
type Manager interface {
	// Installed returns the installed definition; ok is false when none is.
	// A definition that is there but cannot be parsed is an error that
	// wraps ErrUnreadable.
	Installed(ctx context.Context) (d Definition, ok bool, err error)
	// Install writes and registers d so that it starts at login. It never
	// starts the core. changed is false when d was installed already.
	Install(ctx context.Context, d Definition) (changed bool, err error)
	// Uninstall unregisters and removes the service; none installed is no error.
	// The caller stops a core the service runs first.
	Uninstall(ctx context.Context) error
	// Start loads the installed definition afresh and starts it.
	Start(ctx context.Context) error
	// Active reports whether the manager is running the service now.
	Active(ctx context.Context) (bool, error)
	// ManagerEnv is the environment the manager gives its services, for ServiceEnv.
	ManagerEnv(ctx context.Context) map[string]string
	// Graphical reports whether a graphical session target is active (Linux; false elsewhere).
	Graphical(ctx context.Context) bool
}

// ErrNoUserManager is returned on Linux when systemctl --user cannot reach a
// user manager, as in a container or a session without one.
var ErrNoUserManager = errors.New("this system has no systemd user manager (systemctl --user does not answer), so Nearhost cannot be a service here. Run nearhost -d from your shell's startup instead.")

// ErrUnreadable is what Installed's error wraps when a service is installed
// but its definition cannot be parsed, as one edited by hand. Such a service
// can still be uninstalled.
var ErrUnreadable = errors.New("the installed service cannot be read")

// unreadableError is an error that says the definition cannot be parsed,
// and why, in the words of the error it wraps.
type unreadableError struct{ err error }

func (e unreadableError) Error() string   { return e.err.Error() }
func (e unreadableError) Unwrap() []error { return []error{ErrUnreadable, e.err} }

// Unreadable wraps err, which says why an installed definition cannot be
// parsed, so that it is ErrUnreadable too. Fakes of Manager use it.
func Unreadable(err error) error { return unreadableError{err} }

// New returns the Manager for this operating system. home is the user's home
// directory, and run is how commands are run (ExecRunner when nil).
func New(names Names, home string, run Runner) Manager {
	if run == nil {
		run = ExecRunner
	}
	return newManager(names, home, run)
}

// ExecRunner runs the command and returns its combined output. On Windows it
// opens no console window.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	return cmd.CombinedOutput()
}

// output formats a failed command's output for an error message.
func output(b []byte) string { return strings.TrimSpace(string(b)) }

// unchanged reports whether the installed definition, as parsed from a
// format-f file, is what d would install. It compares definitions rather
// than bytes, as a format drops fields it cannot carry.
func unchanged(got Definition, ok bool, d Definition, f Format) bool {
	return ok && reflect.DeepEqual(got, d.For(f))
}
