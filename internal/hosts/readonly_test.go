package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A read-only hosts file is refused before any temporary file exists, with a
// message that says how to clear the attribute. The check is replaced so the
// wording is tested on every system.
func TestApplyReadOnlySaysHowToClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	const content = "127.0.0.1 localhost\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := isReadOnly
	isReadOnly = func(string) bool { return true }
	t.Cleanup(func() { isReadOnly = old })

	// The message names the resolved file, which differs from path where
	// the temp directory is a link or a short name (macOS, Windows runners).
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	err = Apply(path, []string{"a.test"})
	want := target + " is read-only. Clear it with: attrib -r \"" + target + "\" (in an administrator terminal), or run with --no-hosts."
	if err == nil || err.Error() != want || !errors.Is(err, ErrReadOnly) {
		t.Fatalf("err = %v, want %q wrapping ErrReadOnly", err, want)
	}
	if got, _ := os.ReadFile(path); string(got) != content {
		t.Fatalf("file changed: %q", got)
	}
	tempLeftovers(t, dir, "hosts")
}

// A read-only file whose block is already current needs no write, so it is no
// error.
func TestApplyReadOnlyFileWithCurrentBlockIsNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	old := isReadOnly
	isReadOnly = func(string) bool { return true }
	t.Cleanup(func() { isReadOnly = old })
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatalf("err = %v, want none", err)
	}
}
