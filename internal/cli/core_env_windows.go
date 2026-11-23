//go:build windows

package cli

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// interactiveSession reports whether the core runs in a sign-in session.
// Windows services run in session 0, which has no desktop, so a core there
// cannot open a browser for the user.
func interactiveSession() (bool, error) {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return false, fmt.Errorf("cannot tell which session the core runs in: %w", err)
	}
	return id != 0, nil
}
