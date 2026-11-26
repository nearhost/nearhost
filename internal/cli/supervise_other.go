//go:build !windows

package cli

import "github.com/spf13/cobra"

// superviseService is nil: launchd and systemd restart a service core that
// fails, so the service's process is the core.
var superviseService func(cmd *cobra.Command) error
