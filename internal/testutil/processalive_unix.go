//go:build !windows

package testutil

import "syscall"

// ProcessAlive reports whether a process with pid exists. A zombie counts as
// alive until its parent reaps it.
func ProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
