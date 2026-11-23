//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr points the process's standard error at f. The Go runtime
// looks up the standard error handle when it writes a panic, so a panic of a
// service core, which starts with no console, lands in its log. f must stay
// open for the life of the process.
func redirectStderr(f *os.File) error {
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}
