// Package service decides what a login-time service runs and converts that to
// and from each service manager's file format: a launchd plist, a systemd
// user unit and Task Scheduler XML. It runs no commands and touches no files,
// so every function behaves the same on every operating system.
package service

import (
	"errors"
	"slices"
)

// EnvVar is one environment variable of a service.
type EnvVar struct{ Name, Value string }

// Definition is what a service runs. Parse(Render(d)) equals d for the
// fields a format carries (see For).
type Definition struct {
	Exe       string   // absolute path of nearhost
	Args      []string // after "daemon --started-by service"; tests only
	Env       []EnvVar // in this order; none on Windows
	LogPath   string   // launchd StandardErrorPath, systemd StandardError=append:
	Graphical bool     // Linux: WantedBy graphical-session.target, else default.target
}

// Names are the names a service has in each manager.
type Names struct{ Launchd, Systemd, Task string }

// DefaultNames are the names of the real service. Tests use others, so they
// never touch a user's own.
var DefaultNames = Names{"dev.nearhost.core", "nearhost.service", "Nearhost"}

// ErrUnsupported is returned where there is no service manager to use.
var ErrUnsupported = errors.New("services are supported on macOS, on Linux with systemd, and on Windows 11")

// Format is a service manager's file format.
type Format int

const (
	Plist Format = iota
	Unit
	Task
)

// startedBy is what follows the executable in every service's command line.
var startedBy = []string{"daemon", "--started-by", "service"}

// command is the executable and all its arguments.
func (d Definition) command() []string {
	return slices.Concat([]string{d.Exe}, startedBy, d.Args)
}

// fromCommand is the reverse of command: the executable and Args of a
// command line, or false when it does not start the daemon as a service.
func fromCommand(argv []string) (exe string, args []string, ok bool) {
	if len(argv) < 1+len(startedBy) || !slices.Equal(argv[1:1+len(startedBy)], startedBy) {
		return "", nil, false
	}
	if rest := argv[1+len(startedBy):]; len(rest) > 0 {
		args = slices.Clone(rest)
	}
	return argv[0], args, true
}

// For is d reduced to the fields format f carries, which is what Parse
// returns for a rendered d. A launchd plist has no graphical flag; Task
// Scheduler runs with the user's own environment and has no log path.
// Compare an installed definition with a wanted one through this.
func (d Definition) For(f Format) Definition {
	if len(d.Args) == 0 {
		d.Args = nil
	}
	if len(d.Env) == 0 {
		d.Env = nil
	}
	switch f {
	case Plist:
		d.Graphical = false
	case Task:
		d.Env, d.LogPath, d.Graphical = nil, "", false
	}
	return d
}
