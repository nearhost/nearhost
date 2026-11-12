//go:build windows

package cli

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	// elevationNote is the help text's line about how a command asks for
	// the rights it needs.
	elevationNote = "Windows asks you to approve the change."
	// cleanShort is the short help of `hosts clean`.
	cleanShort = "Remove the managed block (Windows asks you to approve it)"
	// configFlagUsage is the usage of the --config flag.
	configFlagUsage = "config file (default %APPDATA%\\nearhost\\config.yaml, or $XDG_CONFIG_HOME\\nearhost\\config.yaml when XDG_CONFIG_HOME is an absolute path)"
	// trustStore names the store `nearhost trust` adds the local CA to: the
	// current user's root store, which needs no elevation.
	trustStore = "your Windows certificate store"
	// trustInstalling is what `nearhost trust` prints before it asks.
	trustInstalling = "Installing Nearhost Local CA. Windows shows a security warning to confirm it: choose Yes."
	// trustDeclined is how a trust or untrust error names a refusal.
	trustDeclined = "if you chose No in the security warning or Windows refused"
	// hostsFix is doctor's fix for a stale hosts block.
	hostsFix = "run `nearhost up`; it updates the block and Windows asks you to approve it"
)

// systemSudo is the production findSudo. Windows needs no sudo for trust:
// the trust store library adds the CA to the current user's root store
// without elevation, and Windows asks the user to confirm it, so there is
// nothing to find and it always returns nil.
func systemSudo() error { return nil }

// trustRefused reports whether err is the user, or Windows, declining the
// security warning. The trust store library flattens the errno into text,
// so the message is checked as well.
func trustRefused(err error) bool {
	return errors.Is(err, windows.ERROR_CANCELLED) || strings.Contains(err.Error(), "canceled by the user")
}
