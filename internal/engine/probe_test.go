package engine

import (
	"net"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

// loopbackListener finds a program that listens on 127.0.0.1, and finds
// nothing on a port where nothing listens.
func TestLoopbackListenerFindsAHolder(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if got, want := loopbackListener(bg, port), l.Addr().String(); got != want {
		t.Errorf("loopbackListener(%d) = %q, want %q", port, got, want)
	}
	free := testutil.ProxyPort(t)
	if got := loopbackListener(bg, free); got != "" {
		t.Errorf("loopbackListener(%d) on a free port = %q, want none", free, got)
	}
}

// Only a probe on the wildcard address dials loopback afterwards.
func TestIsWildcard(t *testing.T) {
	for addr, want := range map[string]bool{
		":443":           true,
		"0.0.0.0:443":    true,
		"[::]:443":       true,
		"127.0.0.1:8080": false,
		"[::1]:8080":     false,
		"443":            false,
	} {
		if got := isWildcard(addr); got != want {
			t.Errorf("isWildcard(%q) = %v, want %v", addr, got, want)
		}
	}
}
