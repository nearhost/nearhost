//go:build unix

package testutil

import (
	"io/fs"
	"os"
	"testing"
)

// WantPerm fails the test unless path has the permission bits want.
func WantPerm(t testing.TB, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode %v, want %v", path, got, want)
	}
}

// LongPath returns p; only Windows has short path forms.
func LongPath(t testing.TB, p string) string { return p }

// Symlink creates link pointing at target.
func Symlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
