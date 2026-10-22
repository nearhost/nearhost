package oserr

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock reports its own WSAE codes, not the POSIX ones syscall defines for
// Windows, so those would never match. Reading a directory fails with
// ERROR_INVALID_FUNCTION; a path beneath a regular file is
// ERROR_PATH_NOT_FOUND, which callers see as fs.ErrNotExist.
var (
	addrInUse        = []syscall.Errno{windows.WSAEADDRINUSE}
	noLoopbackFamily = []syscall.Errno{windows.WSAEADDRNOTAVAIL, windows.WSAEAFNOSUPPORT}
	nothingListening = []syscall.Errno{windows.WSAECONNREFUSED, windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND}
	crossDevice      = []syscall.Errno{windows.ERROR_NOT_SAME_DEVICE}
	notAFile         = []syscall.Errno{syscall.EISDIR, syscall.ENOTDIR, windows.ERROR_INVALID_FUNCTION}
)
