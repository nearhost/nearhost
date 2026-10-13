//go:build linux

package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const testAttr = "user.nearhost_test"

// userXattrFile creates a file carrying testAttr, skipping the test when the
// temp filesystem does not support user extended attributes.
func userXattrFile(t *testing.T, dir, name, content, value string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(p, testAttr, []byte(value), 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("temp filesystem does not allow user xattrs: %v", err)
		}
		t.Fatal(err)
	}
	return p
}

func getAttr(t *testing.T, path string) (string, error) {
	t.Helper()
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, testAttr, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func TestCopyXattrsCopiesPresentAttribute(t *testing.T) {
	dir := t.TempDir()
	src := userXattrFile(t, dir, "src", "a", "hello")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(dst, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyXattrs(src, dst, []string{testAttr}); err != nil {
		t.Fatal(err)
	}
	if got, err := getAttr(t, dst); err != nil || got != "hello" {
		t.Fatalf("attribute = %q, %v; want hello", got, err)
	}
}

func TestCopyXattrsSkipsAbsentAttribute(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	for _, p := range []string{src, dst} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyXattrs(src, dst, []string{testAttr}); err != nil {
		t.Fatalf("absent attribute must be skipped: %v", err)
	}
	if _, err := getAttr(t, dst); err == nil {
		t.Fatal("attribute appeared on destination")
	}
}

func TestApplyCarriesAttributeOver(t *testing.T) {
	dir := t.TempDir()
	path := userXattrFile(t, dir, "hosts", base, "label")
	old := preservedAttrs
	t.Cleanup(func() { preservedAttrs = old })
	preservedAttrs = []string{testAttr}
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	if got, err := getAttr(t, path); err != nil || got != "label" {
		t.Fatalf("attribute after Apply = %q, %v; want label", got, err)
	}
}

func TestApplyFailedLabelCopyAbortsBeforeRename(t *testing.T) {
	dir := t.TempDir()
	path := userXattrFile(t, dir, "hosts", base, "label")
	old, oldSet, oldRename := preservedAttrs, setXattr, renameFile
	t.Cleanup(func() { preservedAttrs, setXattr, renameFile = old, oldSet, oldRename })
	preservedAttrs = []string{testAttr}
	setXattr = func(string, string, []byte, int) error { return syscall.EPERM }
	renameFile = func(string, string) error {
		t.Error("rename reached after a failed label copy")
		return nil
	}
	err := Apply(path, []string{"a.test"})
	if err == nil || !strings.Contains(err.Error(), testAttr) {
		t.Fatalf("err = %v, want one naming %s", err, testAttr)
	}
	if got, _ := os.ReadFile(path); string(got) != base {
		t.Fatalf("original changed: %q", got)
	}
	tempLeftovers(t, dir, "hosts")
}
