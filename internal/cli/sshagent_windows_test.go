//go:build windows

package cli

import "testing"

func TestSSHAgentServiceOnWindows(t *testing.T) {
	state, err := querySSHAgentService()
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	switch state {
	case "running", "stopped", "disabled", "missing":
	default:
		t.Fatalf("state = %q", state)
	}
}
