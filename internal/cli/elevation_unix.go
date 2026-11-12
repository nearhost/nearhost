//go:build unix

package cli

import (
	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/sudo"
)

const (
	// elevationNote is the help text's line about how a command asks for
	// the rights it needs.
	elevationNote = "Asks for your password, as it runs sudo."
	// cleanShort is the short help of `hosts clean`.
	cleanShort = "Remove the managed block (prompts for sudo)"
	// configFlagUsage is the usage of the --config flag.
	configFlagUsage = "config file (default $XDG_CONFIG_HOME/nearhost/config.yaml, or ~/.config/nearhost/config.yaml when XDG_CONFIG_HOME is not an absolute path)"
	// trustStore names the store `nearhost trust` adds the local CA to.
	trustStore = "the system trust store"
	// trustInstalling is what `nearhost trust` prints before it asks.
	trustInstalling = "Installing Nearhost Local CA. You may be asked for your password."
	// trustDeclined is how a trust or untrust error names a refusal.
	trustDeclined = "if you declined the password prompt or the OS refused"
	// hostsFix is doctor's fix for a stale hosts block.
	hostsFix = "run `nearhost up`; it updates the block and asks for your password"
)

// systemSudo is the production findSudo.
func systemSudo() error {
	if elevate.Elevated() {
		return nil
	}
	_, err := sudo.Binary()
	return err
}

// trustRefused is always false here: sudo's refusals keep the generic wording.
func trustRefused(error) bool { return false }
