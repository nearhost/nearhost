//go:build !windows

package config

// renameBusy reports whether a rename failed in a way that passes. Only
// Windows refuses to rename over a file that is open.
func renameBusy(error) bool { return false }
