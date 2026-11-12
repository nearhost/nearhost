//go:build windows

package hosts

import (
	"os"
	"path/filepath"
)

// DefaultPath is the system hosts file.
var DefaultPath = defaultPath()

// defaultPath returns the hosts file under %SystemRoot%, or under C:\Windows
// when that variable is unset.
func defaultPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
}
