// Package elevate runs the steps that need administrator rights, such as
// writing the hosts file. On macOS and Linux that is sudo; the Elevator
// hides which, so the callers do not know.
package elevate

import (
	"context"
	"errors"
)

// Command is a privileged run of Path (an absolute path) with Args.
type Command struct {
	Path        string
	Args        []string
	Input       string // sudo: standard input; UAC: a file named by --input
	Prompt      string // sudo's password prompt, used when Interactive
	Interactive bool   // sudo may prompt on the terminal; false is sudo -n
}

// ErrDeclined is Run's error when the user declined the elevation prompt.
var ErrDeclined = errors.New("the Windows prompt was declined")

// Elevator is how privileged steps are run on this OS.
type Elevator interface {
	// Needed reports whether steps go through Run.
	Needed() bool
	// TakesPassword reports whether Nearhost collects the password itself
	// (sudo does; a UAC dialog does not).
	TakesPassword() bool
	// Cached reports whether Run would not prompt.
	Cached(ctx context.Context) bool
	// Validate checks password and caches the credentials it unlocks.
	Validate(ctx context.Context, password string) error
	// Run runs c with administrator rights.
	Run(ctx context.Context, c Command) error
}
