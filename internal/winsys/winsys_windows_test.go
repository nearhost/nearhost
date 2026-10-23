package winsys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestToolIsInTheSystemDirectoryOnWindows(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	path := Tool("netstat.exe")
	if want := filepath.Join(dir, "netstat.exe"); !strings.EqualFold(path, want) {
		t.Fatalf("Tool(netstat.exe) = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
