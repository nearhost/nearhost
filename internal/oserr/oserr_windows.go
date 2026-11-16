package oserr

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

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

// deletePending opens path through NtCreateFile, which reports the NT
// status that Win32 folds into ACCESS_DENIED: STATUS_DELETE_PENDING.
func deletePending(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	switch {
	case strings.HasPrefix(abs, `\\?\`):
		abs = `\??\` + abs[4:]
	case strings.HasPrefix(abs, `\\`):
		abs = `\??\UNC\` + abs[2:]
	default:
		abs = `\??\` + abs
	}
	name, err := windows.NewNTUnicodeString(abs)
	if err != nil {
		return false
	}
	oa := windows.OBJECT_ATTRIBUTES{ObjectName: name}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, 0, 0, 0)
	if err == nil {
		windows.CloseHandle(h)
		return false
	}
	return errors.Is(err, windows.STATUS_DELETE_PENDING)
}
