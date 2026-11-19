package engine

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// instanceLockOffset is where the byte the lock covers sits, far past the
// pid. A LockFileEx lock keeps other handles from reading the bytes it
// covers, and a second engine reads the pid to name the holder. Windows
// allows a lock past the end of the file.
const instanceLockOffset = 1 << 32

// lockRange is the Overlapped that places the lock at instanceLockOffset.
func lockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: uint32(instanceLockOffset & 0xffffffff), OffsetHigh: uint32(instanceLockOffset >> 32)}
}

// tryLockFile takes an exclusive LockFileEx lock on f without waiting, as
// internal/config's lockFile does with waiting. held reports that another
// handle to the file, in this process or another, has it.
func tryLockFile(f *os.File) (held bool, err error) {
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	return false, err
}

// unlockFile releases the lock tryLockFile took on f.
func unlockFile(f *os.File) {
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
