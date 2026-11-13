package testutil

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// WantPerm only checks that path exists. Go reports every Windows file as
// 0666 or 0444; privacy comes from the profile folder's ACL, which new files
// inherit.
func WantPerm(t testing.TB, path string, want fs.FileMode) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s: %v", path, err)
	}
}

// LongPath returns the long form of p, expanding 8.3 short names such as the
// runner's C:\Users\RUNNER~1.
func LongPath(t testing.TB, p string) string {
	t.Helper()
	in, err := windows.UTF16FromString(p)
	if err != nil {
		t.Fatal(err)
	}
	n, err := windows.GetLongPathName(&in[0], nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uint16, n)
	n, err = windows.GetLongPathName(&in[0], &out[0], uint32(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	return windows.UTF16ToString(out[:n])
}

// Symlink creates link pointing at target, skipping the test when this
// Windows account may not create symlinks.
func Symlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("this Windows account may not create symlinks")
		}
		t.Fatal(err)
	}
}
