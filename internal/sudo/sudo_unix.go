//go:build unix

package sudo

import (
	"io/fs"
	"syscall"
)

// OwnedByRoot reports whether info describes a file owned by uid 0. Tests
// replace it.
var OwnedByRoot = func(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

// runnable reports whether this user may execute path, as the kernel decides
// from its mode, owner and group.
func runnable(path string) bool {
	const xOK = 1 // access(2)'s X_OK
	return syscall.Access(path, xOK) == nil
}
