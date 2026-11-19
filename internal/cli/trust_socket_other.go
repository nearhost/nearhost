//go:build !windows

package cli

// refusedHere is false: off Windows, nothingListens's errors cover every
// way a dial says that nothing listens.
func refusedHere(error) bool { return false }
