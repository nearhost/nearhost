package testutil

import (
	"fmt"
	"net"
	"testing"
)

func TestProxyPortStaysOutsideTheEphemeralRanges(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 40; i++ {
		port := ProxyPort(t)
		// Linux hands out 32768-60999 and macOS 49152-65535 to outgoing
		// connections and ":0" listeners.
		if port < 20000 || port >= 30000 {
			t.Fatalf("ProxyPort = %d, want a port in 20000-29999", port)
		}
		if seen[port] {
			t.Fatalf("ProxyPort returned %d twice", port)
		}
		seen[port] = true
	}
	https, http := ProxyPorts(t)
	if https == http || seen[https] || seen[http] {
		t.Fatalf("ProxyPorts = %d, %d: not distinct from each other and from earlier ports", https, http)
	}
}

func TestProxyPortSkipsAPortSomethingHolds(t *testing.T) {
	// The port the next call would hand out if it were free.
	next := func() int { return proxyPortBase() + int(proxyPortNext.Load())%proxyPortBlock }

	held := next()
	if l, err := net.Listen("tcp", fmt.Sprintf(":%d", held)); err == nil {
		defer l.Close()
	} // else another process holds it, which serves as well
	if got := ProxyPort(t); got == held {
		t.Fatalf("ProxyPort returned %d, which is taken on TCP", got)
	}

	held = next()
	if pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", held)); err == nil {
		defer pc.Close()
	}
	if got := ProxyPort(t); got == held {
		t.Fatalf("ProxyPort returned %d, which is taken on UDP", got)
	}
}
