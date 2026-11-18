//go:build !windows

package config

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// renameBusy reports whether a rename failed in a way that passes. Only
// Windows refuses to rename over a file that is open.
func renameBusy(error) bool { return false }

// syncFile is (*os.File).Sync, replaced in tests to fail as a file system
// might.
var syncFile = (*os.File).Sync

// syncDir syncs the directory dir, so a rename in it survives a power loss.
// A file system that cannot sync a directory, as some FUSE and network
// mounts report, is no error: the file is already in place and there is
// nothing more to do. Any other failure, such as an I/O error, is returned.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	defer f.Close()
	if err := syncFile(f); err != nil && !syncUnsupported(err) {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

// syncUnsupported reports whether a sync failed because the file system
// does not support syncing a directory.
func syncUnsupported(err error) bool {
	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.ENOSYS} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
