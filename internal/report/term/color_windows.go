//go:build windows

package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableColor turns on escape sequences in a Windows console and reports
// whether it has them. Windows Terminal has them already; an old console
// that cannot gets plain text.
func enableColor(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
