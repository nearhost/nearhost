//go:build !windows

package main

import "os"

// watchParent returns a function that reports whether the process that
// started this one has ended: its parent id then differs from the one now.
func watchParent() func() bool {
	ppid := os.Getppid()
	return func() bool { return os.Getppid() != ppid }
}
