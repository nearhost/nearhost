package documents

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openedPath returns the path of the open file f, which macOS gives through
// fcntl's F_GETPATH.
func openedPath(f *os.File) (string, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return "", err
	}
	buf := make([]byte, unix.PathMax)
	var errno unix.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_FCNTL, fd, unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	}); err != nil {
		return "", err
	}
	if errno != 0 {
		return "", errno
	}
	return unix.ByteSliceToString(buf), nil
}
