//go:build unix

package transport

import (
	"os"
	"syscall"
)

// processGone reports whether no process has the given pid.
func processGone(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return proc.Signal(syscall.Signal(0)) != nil
}
