package documents

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileNameNormalizedDOS is GetFinalPathNameByHandle's flags
// FILE_NAME_NORMALIZED | VOLUME_NAME_DOS, both 0, which x/sys/windows doesn't
// name: the normalized path, on a drive letter where there is one.
const fileNameNormalizedDOS = 0

// openedPath returns the path of the open file f, which Windows gives through
// GetFinalPathNameByHandle in the \\?\C:\x form that cleanFinalPath undoes.
func openedPath(f *os.File) (string, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return "", err
	}
	var p string
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		var n uint32
		n, gerr = windows.GetFinalPathNameByHandle(windows.Handle(fd), &buf[0], uint32(len(buf)), fileNameNormalizedDOS)
		if gerr == nil && n >= uint32(len(buf)) {
			gerr = windows.ERROR_NOT_ENOUGH_MEMORY
		}
		if gerr == nil {
			p = windows.UTF16ToString(buf[:n])
		}
	}); err != nil {
		return "", err
	}
	return p, gerr
}
