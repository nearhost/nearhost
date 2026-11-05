//go:build windows

package cli

const (
	// elevationNote is the help text's line about how a command asks for
	// the rights it needs.
	elevationNote = "Windows asks you to approve the change."
	// cleanShort is the short help of `hosts clean`.
	cleanShort = "Remove the managed block (Windows asks you to approve it)"
	// configFlagUsage is the usage of the --config flag.
	configFlagUsage = "config file (default %APPDATA%\\nearhost\\config.yaml)"
)

// systemSudo is the production findSudo. Windows has no sudo to find: the
// trust store library adds the CA to the current user's store itself. It is
// nil, which tells the trust command to skip the sudo check.
var systemSudo func() error
