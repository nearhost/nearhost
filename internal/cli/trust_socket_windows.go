package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

// refusedHere reports whether a dial failed as Windows says that nothing
// listens at a Unix socket's path.
func refusedHere(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
