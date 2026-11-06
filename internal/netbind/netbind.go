// Package netbind binds the ports Nearhost checks and serves on. On Windows a
// plain bind to a port succeeds beside another socket that holds it on
// 127.0.0.1, [::1] or an IPv4-only wildcard, so a port that looks free is not;
// the binds here ask Windows for exclusive use of the port, which fails against
// every holder. On Unix they are the net package's own.
package netbind

import (
	"net"
	"strconv"
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
	p := strconv.Itoa(port)
	hosts := serveHosts()
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = net.JoinHostPort(h, p)
	}
	return out
}
