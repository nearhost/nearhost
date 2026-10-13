//go:build !linux

package hosts

// copyLabels is a no-op off Linux: macOS ACLs need the C acl API.
func copyLabels(src, dst string) error { return nil }
