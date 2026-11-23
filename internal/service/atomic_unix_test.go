//go:build darwin || linux

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service file that is a link, as a dotfiles folder makes, is written
// through: the link stays, and the file it leads to gets the new text.
func TestWriteFileAtomicKeepsALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "nearhost.service")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "user", "nearhost.service")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Errorf("target holds %q, want new", b)
	}

	// A link to a file that does not exist is an error, not a new file in
	// the link's place.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("new"), 0o644); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %v, want one saying the link's file does not exist", err)
	}
}
