// Package netbind binds the ports Nearhost checks and serves on. On Windows a
// plain bind to a port succeeds beside another socket that holds it on
// 127.0.0.1, [::1] or an IPv4-only wildcard, so a port that looks free is not;
// the binds here ask Windows for exclusive use of the port, which fails against
// every holder. On Unix they are the net package's own.
package netbind

import (
	"net"
	"strconv"
	"sync"
)

// LoopbackProbe returns the address to bind to learn whether a loopback port is
// free before forwarding it. On Unix a bind to 127.0.0.1 sees every holder that
// matters. On Windows it would miss a holder on the wildcard address even when
// that holder is exclusive, so it is the wildcard address there.
func LoopbackProbe(port int) string {
	return loopbackHost + ":" + strconv.Itoa(port)
}

// ServeAddrs returns the addresses the proxy serves port on, and so the ones
// to bind to learn whether it is free. On Unix it is the wildcard address: a
// non-root process on macOS can bind a low port only there, and the proxy
// drops clients that aren't on loopback or in a site's extra ranges. On
// Windows it is 127.0.0.1 and [::1], as a program that listens on any other
// address makes Windows ask the user to let it through the firewall, and
// Docker Desktop delivers container requests from loopback. A machine
// without the IPv6 loopback serves on 127.0.0.1 alone.
func ServeAddrs(port int) []string {
	return joinPort(serveHosts(), port)
}

// LoopbackAddrs returns the loopback addresses of port: 127.0.0.1 and [::1],
// or 127.0.0.1 alone on a machine without the IPv6 loopback, where a listen
// on [::1] would fail and take the rest of the proxy's configuration with it.
// Nearhost's page listens on these on every system, so the kernel refuses a
// client on any other machine before a request is read.
func LoopbackAddrs(port int) []string {
	return joinPort(loopbackHosts(), port)
}

// loopbackHosts are the hosts LoopbackAddrs binds.
func loopbackHosts() []string {
	if hasIPv6Loopback() {
		return []string{"127.0.0.1", "::1"}
	}
	return []string{"127.0.0.1"}
}

// hasIPv6Loopback reports whether this machine can listen on [::1], which a
// container or a system with IPv6 turned off cannot. It asks once. Tests
// replace it to stand for a machine without the IPv6 loopback.
var hasIPv6Loopback = sync.OnceValue(func() bool {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
})

// joinPort returns each of hosts joined with port.
func joinPort(hosts []string, port int) []string {
	p := strconv.Itoa(port)
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = net.JoinHostPort(h, p)
	}
	return out
}
