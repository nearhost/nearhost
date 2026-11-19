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

// IsSystemPath reports whether path is absolute and, cleaned, names
// SystemPath() without regard to case, as NTFS compares names. Windows
// removes ".." elements from a path before it opens it, so the cleaned path
// is the file that opens. A path that is not absolute, which includes one
// with no drive, is refused, as it depends on the working directory.
func IsSystemPath(path string) bool {
	return filepath.IsAbs(path) && strings.EqualFold(filepath.Clean(path), SystemPath())
}
