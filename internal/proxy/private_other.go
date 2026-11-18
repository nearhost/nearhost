//go:build !windows

package proxy

// restrictStorage is nil here: the storage directory is created 0700, and
// that mode is what keeps it private. Tests replace it.
var restrictStorage func(root string) error
