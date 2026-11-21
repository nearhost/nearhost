//go:build unix

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/hosts"
)

// With no terminal sudo cannot ask for a password, so an update that needs
// one fails and says to run the command in a terminal.
func TestHostsUpdateNeedsATerminalForSudo(t *testing.T) {
	h := newUpdateHarness(t, updateConfig(), "127.0.0.1 localhost\n")
	h.terminal = false
	e := &upElevator{err: errors.New("sudo: a password is required")}
	newHostsUpdateApplier = func(ctx context.Context, terminal bool) hosts.Applier {
		if terminal {
			t.Error("the applier was told there is a terminal")
		}
		return upHostsApplier(ctx, e, "/usr/local/bin/nearhost", terminal)
	}
	out, err := h.run()
	if err == nil || !errors.Is(err, errHostsUpdateNoTerminal) {
		t.Fatalf("hosts update returned %v, want %q (output %q)", err, errHostsUpdateNoTerminal, out)
	}
	if errHostsUpdateNoTerminal.Error() != "The hosts file needs sudo, and there is no terminal to ask in. Run nearhost hosts update in a terminal." {
		t.Errorf("the message reads %q", errHostsUpdateNoTerminal)
	}
}
