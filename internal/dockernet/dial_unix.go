//go:build !windows

package dockernet

import (
	"context"
	"net"
)

// dial connects to the daemon's unix socket at endpoint.
func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", endpoint)
}
