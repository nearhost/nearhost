//go:build unix

package engine

import (
	"os"
	"syscall"
)

// tryLockFile takes an exclusive flock on f without waiting, as
// internal/config's lockFile does with waiting. held reports that another
// open of the file, in this process or another, has it.
func tryLockFile(f *os.File) (held bool, err error) {
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EINTR {
			break
		}
	}
	if err == syscall.EWOULDBLOCK {
		return true, nil
	}
	return false, err
}

// unlockFile releases the lock tryLockFile took on f.
func unlockFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
