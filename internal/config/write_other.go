//go:build !windows

package config

import (
	"fmt"
	"os"
)

// renameBusy reports whether a rename failed in a way that passes. Only
// Windows refuses to rename over a file that is open.
func renameBusy(error) bool { return false }

// syncDir syncs the directory dir, so a rename in it survives a power loss.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
