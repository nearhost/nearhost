//go:build !windows

package config

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// A directory sync the file system does not support is ignored; an I/O
// error is returned.
func TestSyncDirIgnoresOnlyAnUnsupportedSync(t *testing.T) {
	saved := syncFile
	t.Cleanup(func() { syncFile = saved })
	// ENOTSUP and EOPNOTSUPP are one number on Linux and two on macOS.
	cases := []struct {
		errno   syscall.Errno
		wantErr bool
	}{{syscall.EINVAL, false}, {syscall.ENOTSUP, false}, {syscall.EOPNOTSUPP, false}, {syscall.ENOSYS, false}, {syscall.EIO, true}}
	for _, tc := range cases {
		errno, wantErr := tc.errno, tc.wantErr
		syncFile = func(f *os.File) error { return &os.PathError{Op: "sync", Path: f.Name(), Err: errno} }
		err := syncDir(t.TempDir())
		if got := err != nil; got != wantErr {
			t.Errorf("%v: syncDir error = %v, want an error: %v", errno, err, wantErr)
		}
		if wantErr && !errors.Is(err, errno) {
			t.Errorf("%v: error %v does not keep the cause", errno, err)
		}
	}
}
