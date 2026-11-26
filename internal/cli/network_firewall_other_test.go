//go:build !windows

package cli

import (
	"bytes"
	"testing"
)

// Only Windows asks for a firewall rule; elsewhere the command says so.
func TestFirewallCommandNothingToDoOffWindows(t *testing.T) {
	for goos, want := range map[string]string{"linux": "Nothing to do on Linux.\n", "darwin": "Nothing to do on macOS.\n"} {
		orig := firewallGOOS
		firewallGOOS = goos
		root := NewRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"network", "firewall"})
		err := root.Execute()
		firewallGOOS = orig
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if out.String() != want {
			t.Errorf("%s: printed %q, want %q", goos, out.String(), want)
		}
	}
}
