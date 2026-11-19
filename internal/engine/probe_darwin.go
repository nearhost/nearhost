package engine

import "context"

// loopbackHeld returns the loopback address where a program listens on port,
// or "" when none does. On macOS a bind to the wildcard address succeeds
// beside a program that holds the port on 127.0.0.1 or [::1], and that
// program, bound to the more specific address, then gets every connection to
// loopback, so probeAddr asks this after its wildcard bind succeeds. Call it
// only once the probe's own listener is closed, or it finds the probe.
func loopbackHeld(ctx context.Context, port int) string {
	return loopbackListener(ctx, port)
}
