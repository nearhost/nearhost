package hosts

import (
	"os"
	"path/filepath"
	"testing"
)

// The default path that Nearhost hands the hosts helper is the system hosts
// file, which IsSystemPath accepts.
func TestDefaultPathIsTheSystemPath(t *testing.T) {
	if DefaultPath != SystemPath() {
		t.Fatalf("DefaultPath = %q, SystemPath() = %q", DefaultPath, SystemPath())
	}
	if !IsSystemPath(DefaultPath) {
		t.Fatalf("IsSystemPath(%q) = false", DefaultPath)
	}
}

// Any other file, including a relative path and a link, is not the system
// hosts file.
func TestIsSystemPathRefusesOtherFiles(t *testing.T) {
	sys := SystemPath()
	dir, base := filepath.Dir(sys), filepath.Base(sys)
	tmp := t.TempDir()
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(filepath.Join(tmp, "other"), link); err != nil {
		t.Logf("no symlink: %v", err)
	}
	for _, p := range []string{
		"",
		base,
		filepath.Join("etc", base),
		dir,
		sys + ".bak",
		filepath.Join(tmp, "hosts"),
		link,
	} {
		if IsSystemPath(p) {
			t.Errorf("IsSystemPath(%q) = true, want false", p)
		}
	}
}
