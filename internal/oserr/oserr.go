// Package oserr names error conditions that the operating systems report
// with different codes, so one check holds on macOS, Linux and Windows.
package oserr

import (
	"errors"
	"syscall"
)

// AddrInUse reports whether err says the address is already taken.
func AddrInUse(err error) bool { return isAny(err, addrInUse) }

// NoLoopbackFamily reports whether err says the machine has no such address
// or address family, as when it has no IPv6 loopback.
func NoLoopbackFamily(err error) bool { return isAny(err, noLoopbackFamily) }

// NothingListening reports whether err says nothing listens at a socket:
// there is no file, or one left behind by a server that has gone.
func NothingListening(err error) bool { return isAny(err, nothingListening) }

// CrossDevice reports whether err says a rename crossed filesystems.
func CrossDevice(err error) bool { return isAny(err, crossDevice) }

// NotAFile reports whether err says a path names a directory, or lies under
// a regular file, where a file was wanted.
func NotAFile(err error) bool { return isAny(err, notAFile) }

func isAny(err error, codes []syscall.Errno) bool {
	for _, code := range codes {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}
