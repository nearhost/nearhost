//go:build !darwin

package engine

import "context"

// loopbackHeld finds nothing away from macOS: on Linux a bind to the wildcard
// address fails while any program holds the port on loopback, and on Windows
// the proxy binds loopback itself, exclusively, so the bind probe already
// sees such a program.
func loopbackHeld(context.Context, int) string { return "" }
