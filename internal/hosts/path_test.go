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

// A spelling of the system hosts file that cleans to it is the system hosts
// file; any other file is not.
func TestIsSystemPath(t *testing.T) {
	sys := SystemPath()
	dir, base := filepath.Dir(sys), filepath.Base(sys)
	for _, p := range []string{
		sys + string(filepath.Separator),
		filepath.Join(dir, "..", filepath.Base(dir)) + string(filepath.Separator) + base,
		dir + string(filepath.Separator) + "." + string(filepath.Separator) + base,
	} {
		if !IsSystemPath(p) {
			t.Errorf("IsSystemPath(%q) = false, want true", p)
		}
	}
	tmp := t.TempDir()
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(filepath.Join(tmp, "other"), link); err != nil {
		t.Logf("no symlink: %v", err)
	}
	for _, p := range []string{
		"",
		base,
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
