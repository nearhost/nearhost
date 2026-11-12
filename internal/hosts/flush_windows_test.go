//go:build windows

package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ipconfig fallback runs elevated, so it takes the copy in the system
// directory rather than whichever one PATH finds first.
func TestIpconfigComesFromTheSystemDirectoryOnWindows(t *testing.T) {
	path, err := ipconfigPath()
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("SystemRoot")
	if !strings.EqualFold(path, filepath.Join(root, "System32", "ipconfig.exe")) {
		t.Fatalf("ipconfig at %q, SystemRoot %q", path, root)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
