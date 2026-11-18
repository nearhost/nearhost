package documents

import (
	"os"
	"strconv"
)

// openedPath returns the path of the open file f, which Linux gives as the
// target of /proc/self/fd/N.
func openedPath(f *os.File) (string, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return "", err
	}
	var p string
	var lerr error
	if err := rc.Control(func(fd uintptr) {
		p, lerr = os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10))
	}); err != nil {
		return "", err
	}
	return p, lerr
}
