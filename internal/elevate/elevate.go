// Package elevate runs the steps that need administrator rights, such as
// writing the hosts file. On macOS and Linux that is sudo, and on Windows a
// UAC prompt; the Elevator hides which, so the callers do not know.
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
// Windows reports a prompt that timed out after about 2 minutes as
// ERROR_CANCELLED, just as it reports a No, so it may have gone unanswered.
var ErrDeclined = errors.New("the Windows prompt was declined or went unanswered")

// HelperRefusedCode is the exit code of an elevated `hosts apply` that
// refuses the paths of its --input and --report, before it opens or creates
// either, so it leaves no report behind.
const HelperRefusedCode = 3

// systemOverride, when set, gives the Elevator System returns. Nothing sets
// it in a release: the one file that does is built with the e2e tag alone,
// for the journeys in internal/e2e.
var systemOverride func() Elevator

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
	// Forget drops the credentials Validate cached, without prompting.
	Forget(ctx context.Context)
	// Run runs c with administrator rights.
	Run(ctx context.Context, c Command) error
}
