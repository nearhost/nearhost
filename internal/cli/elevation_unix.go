//go:build unix

package cli

import (
	"context"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/hosts"
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
	hostsFix = "run `nearhost hosts update`; it updates the block and asks for your password"
	// hostsApprove is what up's fix tells the user to do when it next asks
	// to update the hosts file.
	hostsApprove = "approve the password prompt"
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

// upHostsApplier is the applier `nearhost up` updates the hosts file with,
// through e. With terminal (stdin is a terminal) sudo may prompt for the
// password. Without one (a service, CI, nohup, stdin redirected) it never
// prompts: the helper runs through sudo -n, which works only when sudo needs
// no password for it, as with credentials still cached from a terminal run,
// and sudo's refusal for want of a password becomes errNoTerminal. Any other
// failure keeps its own error. As root no sudo runs at all.
//
// sudo -n also succeeds under a NOPASSWD rule for the helper. The code
// tolerates such a rule but does not advise one: the helper is this binary,
// which its user can usually write, so the rule gives that user root. The
// advice for running without a terminal is --no-hosts, after one run at a
// terminal has written the hosts block.
func upHostsApplier(ctx context.Context, e elevate.Elevator, exe string, terminal bool) hosts.Applier {
	apply := hosts.ElevatedApplier(ctx, e, exe, hosts.DefaultPath, hostsPrompt, terminal)
	return func(aliases []string) error {
		err := apply(aliases)
		if err != nil && !terminal && ctx.Err() == nil && sudo.PasswordRequired(err.Error()) {
			return errNoTerminal
		}
		return err
	}
}
