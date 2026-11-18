//go:build !darwin

package engine

import (
	"net"
	"testing"
)

// Away from macOS a bind to the wildcard address already fails against a
// holder on loopback, so the probe does not dial: loopbackHeld finds nothing
// even where something listens.
func TestLoopbackHeldDoesNothingAwayFromMacOS(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if got := loopbackHeld(bg, l.Addr().(*net.TCPAddr).Port); got != "" {
		t.Errorf("loopbackHeld = %q, want none", got)
	}
}
