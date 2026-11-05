package winsys

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Tool is the path of name in the system directory, normally
// C:\Windows\System32, so that a program of the same name earlier in PATH
// is not run instead. When the directory cannot be read it is name alone,
// found through PATH.
func Tool(name string) string {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return name
	}
	return filepath.Join(dir, name)
}
