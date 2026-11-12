//go:build !windows

package cli

// firefoxRootsOff is nil off Windows: only there does Firefox need to be told
// to read the system's trusted roots.
var firefoxRootsOff func() (where string, off bool)
