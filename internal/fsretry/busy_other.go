//go:build !windows

package fsretry

// Busy reports whether a rename failed in a way that passes. Only Windows
// refuses to rename a file that is open.
func Busy(error) bool { return false }

// Sharing reports whether a rename failed because another program has one
// of the files open without sharing it. Only Windows says so.
func Sharing(error) bool { return false }
