//go:build windows

package cli

const (
	// elevationNote is the help text's line about how a command asks for
	// the rights it needs.
	elevationNote = "Windows asks you to approve the change."
	// cleanShort is the short help of `hosts clean`.
	cleanShort = "Remove the managed block (Windows asks you to approve it)"
	// configFlagUsage is the usage of the --config flag.
	configFlagUsage = "config file (default %APPDATA%\\nearhost\\config.yaml, or $XDG_CONFIG_HOME\\nearhost\\config.yaml when XDG_CONFIG_HOME is an absolute path)"
)

// systemSudo is the production findSudo. Windows needs no sudo for trust:
// the trust store library adds the CA to the current user's root store
// without elevation, and Windows asks the user to confirm it, so there is
// nothing to find and it always returns nil.
func systemSudo() error { return nil }
