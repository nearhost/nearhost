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

// Windows paths are compared without case, as NTFS does, and with either
// slash.
func TestIsSystemPathIgnoresCaseOnWindows(t *testing.T) {
	sys := SystemPath()
	for _, p := range []string{strings.ToUpper(sys), strings.ToLower(sys), strings.ReplaceAll(sys, `\`, "/")} {
		if !IsSystemPath(p) {
			t.Errorf("IsSystemPath(%q) = false, want true", p)
		}
	}
	if IsSystemPath(`D:\Win\System32\drivers\etc\hosts`) {
		t.Error("IsSystemPath accepted a hosts file on another drive")
	}
}

func TestFlushDNSOnWindows(t *testing.T) {
	if err := flushDNS(); err != nil {
		t.Fatalf("flushDNS() = %v", err)
	}
}
