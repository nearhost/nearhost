//go:build !windows

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
)

// systemSudo is the production findSudo.
func systemSudo() error {
	if elevate.Elevated() {
		return nil
	}
	_, err := sudo.Binary()
	return err
}
