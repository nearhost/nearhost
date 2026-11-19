//go:build windows

package hosts

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The hosts file is under the system directory Windows reports, whatever
// %SystemRoot% says: the caller of the elevated helper sets that variable.
func TestSystemPathIsUnderTheSystemDirectoryOnWindows(t *testing.T) {
	t.Setenv("SystemRoot", `D:\Win`)
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := SystemPath(), filepath.Join(dir, "drivers", "etc", "hosts"); got != want {
		t.Fatalf("SystemPath() = %q, want %q", got, want)
	}
}

// Windows paths are compared cleaned and without case, as Windows removes
// ".." before it opens a path and NTFS ignores case. A path that is not
// absolute is refused.
func TestIsSystemPathOnWindows(t *testing.T) {
	sys := SystemPath()
	dir := filepath.Dir(sys)
	for _, p := range []string{
		strings.ToUpper(sys),
		strings.ToLower(sys),
		strings.ReplaceAll(sys, `\`, "/"),
		filepath.Dir(dir) + `\..\` + filepath.Base(filepath.Dir(dir)) + `\` + filepath.Base(dir) + `\hosts`,
	} {
		if !IsSystemPath(p) {
			t.Errorf("IsSystemPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		sys[len(filepath.VolumeName(sys)):],
		`drivers\etc\hosts`,
		`D:\Win\System32\drivers\etc\hosts`,
	} {
		if IsSystemPath(p) {
			t.Errorf("IsSystemPath(%q) = true, want false", p)
		}
	}
}

func TestFlushDNSOnWindows(t *testing.T) {
	if err := flushDNS(); err != nil {
		t.Fatalf("flushDNS() = %v", err)
	}
}
