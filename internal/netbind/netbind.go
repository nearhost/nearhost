// Package netbind binds the ports Nearhost checks and serves on. On Windows a
// plain bind to a port succeeds beside another socket that holds it on
// 127.0.0.1, [::1] or an IPv4-only wildcard, so a port that looks free is not;
// the binds here ask Windows for exclusive use of the port, which fails against
// every holder. On Unix they are the net package's own.
package netbind

import "strconv"

// LoopbackProbe returns the address to bind to learn whether a loopback port is
// free before forwarding it. On Unix a bind to 127.0.0.1 sees every holder that
// matters. On Windows it would miss a holder on the wildcard address even when
// that holder is exclusive, so it is the wildcard address there.
func LoopbackProbe(port int) string {
	return loopbackHost + ":" + strconv.Itoa(port)
}
