package netbind

import (
	"slices"
	"testing"
)

// standIPv6Loopback makes hasIPv6Loopback report has for the rest of the
// test.
func standIPv6Loopback(t *testing.T, has bool) {
	t.Helper()
	orig := hasIPv6Loopback
	hasIPv6Loopback = func() bool { return has }
	t.Cleanup(func() { hasIPv6Loopback = orig })
}

// Nearhost's page listens on both loopback addresses where the machine has
// them.
func TestLoopbackAddrsAreBothLoopbacks(t *testing.T) {
	standIPv6Loopback(t, true)
	if got, want := LoopbackAddrs(6327), []string{"127.0.0.1:6327", "[::1]:6327"}; !slices.Equal(got, want) {
		t.Errorf("LoopbackAddrs(6327) = %q, want %q", got, want)
	}
}

// A machine without the IPv6 loopback listens on 127.0.0.1 alone: a listen
// on [::1] there would fail the proxy's whole configuration.
func TestLoopbackAddrsWithoutIPv6Loopback(t *testing.T) {
	standIPv6Loopback(t, false)
	if got, want := LoopbackAddrs(6327), []string{"127.0.0.1:6327"}; !slices.Equal(got, want) {
		t.Errorf("LoopbackAddrs(6327) = %q, want %q", got, want)
	}
}
