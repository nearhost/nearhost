//go:build unix

package oserr

import "syscall"

var (
	addrInUse        = []syscall.Errno{syscall.EADDRINUSE}
	noLoopbackFamily = []syscall.Errno{syscall.EADDRNOTAVAIL, syscall.EAFNOSUPPORT}
	nothingListening = []syscall.Errno{syscall.ENOENT, syscall.ECONNREFUSED}
	crossDevice      = []syscall.Errno{syscall.EXDEV}
	notAFile         = []syscall.Errno{syscall.EISDIR, syscall.ENOTDIR}
)
