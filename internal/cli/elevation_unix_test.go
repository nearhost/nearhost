//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
)

// upElevator stands for sudo: Run records each command and fails with err.
type upElevator struct {
	elevate.Elevator
	err  error
	runs []elevate.Command
}

// Run records c and returns e.err.
func (e *upElevator) Run(_ context.Context, c elevate.Command) error {
	e.runs = append(e.runs, c)
	return e.err
}

// up's hosts update prompts for sudo's password only when stdin is a
// terminal. Without one it runs the helper through sudo -n: a sudoers rule
// that needs no password for it works, sudo's "a password is required" is
// errNoTerminal, and any other failure keeps its own error.
func TestUpHostsApplierPromptsOnlyOnATerminal(t *testing.T) {
	other := errors.New("sudo: unable to resolve host mini: Name or service not known")
	required := errors.New("sudo: a password is required")
	cases := []struct {
		name            string
		terminal        bool
		runErr          error
		wantErr         error
		wantInteractive bool
	}{
		{"terminal", true, nil, nil, true},
		{"terminal, password required", true, required, required, true},
		{"no terminal, no password needed", false, nil, nil, false},
		{"no terminal, password required", false, required, errNoTerminal, false},
		{"no terminal, sudo-rs password required", false, errors.New("sudo: interactive authentication is required"), errNoTerminal, false},
		{"no terminal, another failure", false, other, other, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &upElevator{err: tc.runErr}
			err := upHostsApplier(context.Background(), e, "/usr/local/bin/nearhost", tc.terminal)([]string{"web.test"})
			if (err == nil) != (tc.wantErr == nil) || !errors.Is(err, tc.wantErr) {
				t.Fatalf("applier returned %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != errNoTerminal && errors.Is(err, errNoTerminal) {
				t.Fatalf("applier returned errNoTerminal for %v", tc.runErr)
			}
			if len(e.runs) != 1 || e.runs[0].Interactive != tc.wantInteractive {
				t.Fatalf("runs %+v, want one with Interactive %v", e.runs, tc.wantInteractive)
			}
		})
	}
}

// Without a terminal, every update that sudo refuses for want of a password
// fails, and up says why once.
func TestUpHostsWithoutATerminalSaysSoOnce(t *testing.T) {
	e := &upElevator{err: errors.New("sudo: a password is required")}
	var out bytes.Buffer
	ensure := warnNoTerminal(&out, func(ctx context.Context, aliases []string) error {
		return upHostsApplier(ctx, e, "/usr/local/bin/nearhost", false)(aliases)
	})
	for range 3 {
		if err := ensure(context.Background(), []string{"web.test"}); !errors.Is(err, errNoTerminal) {
			t.Fatalf("ensure returned %v", err)
		}
	}
	if got := out.String(); got != hostsNoTerminal+"\n" {
		t.Errorf("printed %q, want the message once", got)
	}
}
