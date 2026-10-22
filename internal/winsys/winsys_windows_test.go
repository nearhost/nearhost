package winsys

import (
	"os"
	"testing"
)

func TestToolIsInTheSystemDirectoryOnWindows(t *testing.T) {
	path := Tool("netstat.exe")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Tool(netstat.exe) = %q: %v", path, err)
	}
}
