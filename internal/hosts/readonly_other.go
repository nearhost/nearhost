//go:build !windows

package hosts

// readOnly reports false: only Windows has a read-only attribute that stops a
// rename over the file. A hosts file made immutable on Linux fails on its own.
func readOnly(string) bool { return false }
