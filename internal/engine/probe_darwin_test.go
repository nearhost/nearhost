package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/testutil"
)

// probeEngine is an engine with only what probePort uses: the real binds,
// and a holder lookup that names "holder (pid 1)" and records the port.
func probeEngine(looked *int) *Engine {
	return &Engine{
		listen:       netbind.Listen,
		listenPacket: netbind.ListenPacket,
		holderOf: func(_ context.Context, port int) string {
			*looked = port
			return "holder (pid 1)"
		},
	}
}

// assertHeld checks that probing port at the proxy's addresses reports it in
// use, with the holder the lookup named.
func assertHeld(t *testing.T, port int) {
	t.Helper()
	var looked int
	err := probeEngine(&looked).probePort(bg, "HTTP", port, 80, netbind.ServeAddrs)
	want := fmt.Sprintf("HTTP port %d is in use by holder (pid 1)", port)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("probePort = %v, want %q", err, want)
	}
	var pe *portError
	if !errors.As(err, &pe) || !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("%v is not a *portError wrapping EADDRINUSE", err)
	}
	if looked != port {
		t.Errorf("looked up the holder of port %d, want %d", looked, port)
	}
}

// macOS lets a bind to the wildcard address succeed beside a program that
// holds the port on 127.0.0.1, which would then get every request to a
// .test name. The probe dials loopback and reports the port in use.
func TestProbeSeesAHolderOn127001(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	assertHeld(t, l.Addr().(*net.TCPAddr).Port)
}

// A holder on [::1] alone is seen too, on a machine with the IPv6 loopback.
func TestProbeSeesAHolderOnIPv6Loopback(t *testing.T) {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("cannot listen on [::1]: %v", err)
	}
	defer l.Close()
	assertHeld(t, l.Addr().(*net.TCPAddr).Port)
}

// A port nothing listens on is free.
func TestProbeFindsAFreePortFree(t *testing.T) {
	port := testutil.ProxyPort(t)
	var looked int
	if err := probeEngine(&looked).probePort(bg, "HTTP", port, 80, netbind.ServeAddrs); err != nil {
		t.Fatalf("probePort on a free port = %v", err)
	}
	if looked != 0 {
		t.Errorf("looked up a holder of free port %d", looked)
	}
}
