package config

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on the file at path, creating it and its
// directory when missing, and returns what releases it. It waits for any
// other holder, in this process or another, since each open of the file is
// a lock of its own. Unlike on Unix the file's mode is not checked: Windows
// modes carry only the read-only bit, and the profile's ACL keeps it private.
func lockFile(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock: %w", err)
	}
	handle := windows.Handle(f.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{}); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock config: %w", err)
	}
	return func() {
		windows.UnlockFileEx(handle, 0, 1, 0, &windows.Overlapped{})
		f.Close()
	}, nil
}
