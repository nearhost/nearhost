//go:build windows

package hosts

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// DefaultPath is the system hosts file, the path Nearhost hands the
// elevated hosts helper.
var DefaultPath = SystemPath()

// SystemPath returns the hosts file under the system directory Windows
// reports. It does not read %SystemRoot%, which the caller of the elevated
// helper sets. When the call fails, which Windows does not document as
// happening, it falls back to C:\Windows\System32.
func SystemPath() string {
	dir, err := windows.GetSystemDirectory()
	if err != nil || dir == "" {
		dir = `C:\Windows\System32`
	}
	return filepath.Join(dir, "drivers", "etc", "hosts")
}

// samePath reports whether two clean absolute paths name the same entry.
// NTFS compares names without case.
func samePath(a, b string) bool { return strings.EqualFold(a, b) }
