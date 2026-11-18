//go:build unix

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
)

// upElevator stands for sudo: needed unless root, with its credentials cached
// or not, recording what it is asked.
type upElevator struct {
	elevate.Elevator
	needed, cached bool
	asked          int
	runs           []elevate.Command
}

// Needed reports whether the step goes through sudo.
func (e *upElevator) Needed() bool { return e.needed }

// Cached reports whether sudo -n would run without a password, and counts
// the question.
func (e *upElevator) Cached(context.Context) bool { e.asked++; return e.cached }

// Run records c.
func (e *upElevator) Run(_ context.Context, c elevate.Command) error {
	e.runs = append(e.runs, c)
	return nil
}

// up's hosts update prompts for sudo's password only when stdin is a
// terminal. Without one it runs only what sudo -n can run, and otherwise
// refuses without starting sudo.
func TestUpHostsApplierPromptsOnlyOnATerminal(t *testing.T) {
	cases := []struct {
		name                     string
		terminal, needed, cached bool
		wantErr                  error
		wantRun, wantInteractive bool
	}{
		{"terminal", true, true, false, nil, true, true},
		{"no terminal, password needed", false, true, false, errNoTerminal, false, false},
		{"no terminal, credentials cached", false, true, true, nil, true, false},
		{"no terminal, root", false, false, false, nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &upElevator{needed: tc.needed, cached: tc.cached}
			err := upHostsApplier(context.Background(), e, "/usr/local/bin/nearhost", tc.terminal)([]string{"web.test"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("applier returned %v, want %v", err, tc.wantErr)
			}
			if ran := len(e.runs) == 1; ran != tc.wantRun {
				t.Fatalf("ran %d times, want run %v", len(e.runs), tc.wantRun)
			}
			if tc.wantRun && tc.needed && e.runs[0].Interactive != tc.wantInteractive {
				t.Errorf("Interactive = %v, want %v", e.runs[0].Interactive, tc.wantInteractive)
			}
			if tc.terminal && e.asked != 0 {
				t.Errorf("sudo -n was asked %d times with a terminal to prompt on", e.asked)
			}
		})
	}
}
