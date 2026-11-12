//go:build !windows

package testutil

import "syscall"

// processAlive reports whether a process with pid exists. A zombie counts as
// gone only once reaped; the fake's parent is the init process by then.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
