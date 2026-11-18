package engine

import (
	"context"
	"net"
	"strconv"
	"time"
)

// loopbackDialWait bounds each dial of loopbackListener. A loopback port
// where nothing listens refuses at once; only a firewall that drops packets
// on loopback, which is unusual, uses the time up.
const loopbackDialWait = 200 * time.Millisecond

// loopbackListener dials port on 127.0.0.1 and then on [::1], and returns the
// address of the first that accepts, or "" when neither does. A refusal, a
// machine without the IPv6 loopback and a dial that times out all count as
// nothing listening, so a start is never held up by the check. A connection
// is closed as soon as it is made; the program that accepted it may log it.
func loopbackListener(ctx context.Context, port int) string {
	p := strconv.Itoa(port)
	for _, host := range []string{"127.0.0.1", "::1"} {
		addr := net.JoinHostPort(host, p)
		d := net.Dialer{Timeout: loopbackDialWait}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			continue
		}
		_ = conn.Close()
		return addr
	}
	return ""
}

// isWildcard reports whether addr, a host:port, is on the wildcard address.
func isWildcard(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}
