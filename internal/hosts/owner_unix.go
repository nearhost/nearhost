//go:build unix

package hosts

import (
	"io/fs"
	"os"
	"syscall"
)

// chownLike gives f the owner and group recorded in info.
func chownLike(f *os.File, info fs.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return f.Chown(int(st.Uid), int(st.Gid))
	}
	return nil
}
