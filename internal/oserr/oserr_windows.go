package oserr

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock reports its own WSAE codes, not the POSIX ones syscall defines for
// Windows, so those would never match. A bind to a port in a range Hyper-V
// or WinNAT has reserved, or held with SO_EXCLUSIVEADDRUSE, is WSAEACCES:
// taken, as far as Nearhost can tell. Reading a directory fails with
// ERROR_INVALID_FUNCTION; a path beneath a regular file is
// ERROR_PATH_NOT_FOUND, which syscall defines as ENOTDIR and which is also
// fs.ErrNotExist. So is a path whose directory is missing. A file that
// another handle holds open without sharing the access asked for (Go opens
// files without FILE_SHARE_DELETE, so even a reader stops a removal) is
// ERROR_SHARING_VIOLATION, or ERROR_LOCK_VIOLATION for a locked range.
var (
	addrInUse        = []syscall.Errno{windows.WSAEADDRINUSE, windows.WSAEACCES}
	noLoopbackFamily = []syscall.Errno{windows.WSAEADDRNOTAVAIL, windows.WSAEAFNOSUPPORT}
	nothingListening = []syscall.Errno{windows.WSAECONNREFUSED, windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND}
	crossDevice      = []syscall.Errno{windows.ERROR_NOT_SAME_DEVICE}
	notAFile         = []syscall.Errno{syscall.EISDIR, syscall.ENOTDIR, windows.ERROR_INVALID_FUNCTION}
	inUse            = []syscall.Errno{windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION}
)
