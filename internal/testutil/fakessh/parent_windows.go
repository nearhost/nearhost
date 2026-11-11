package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// watchParent returns a function that reports whether the process that
// started this one has ended. It holds a handle on the parent, so a reused
// pid cannot fool it; the handle lives as long as this process.
func watchParent() func() bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(os.Getppid()))
	if err != nil {
		// The parent is already gone, or cannot be watched.
		gone := err == windows.ERROR_INVALID_PARAMETER
		return func() bool { return gone }
	}
	return func() bool {
		ev, err := windows.WaitForSingleObject(h, 0)
		return err == nil && ev == windows.WAIT_OBJECT_0
	}
}
