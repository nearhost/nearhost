//go:build !windows

package cli

import "os"

// redirectStderr does nothing: launchd and systemd already send a service's
// standard error to the core log.
func redirectStderr(*os.File) error { return nil }
