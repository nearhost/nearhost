package elevate

import (
	"context"
	"errors"
)

// System is this OS's Elevator.
func System() Elevator { return windowsElevator{} }

// windowsElevator is a stand-in until elevation through UAC is built: it
// says whether steps would need it and runs nothing.
type windowsElevator struct{}

// Needed reports whether this process is not elevated.
func (windowsElevator) Needed() bool { return !Elevated() }

// TakesPassword is false: Windows asks for consent itself.
func (windowsElevator) TakesPassword() bool { return false }

// Cached is false: there are no credentials to hold.
func (windowsElevator) Cached(context.Context) bool { return false }

// Validate has no password to check.
func (windowsElevator) Validate(context.Context, string) error { return nil }

// Run fails: elevation on Windows is not built yet.
func (windowsElevator) Run(context.Context, Command) error {
	return errors.New("elevation on Windows is not built yet")
}
