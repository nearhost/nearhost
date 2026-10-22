package desktop

import (
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOpenCommandOnWindows(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	url := "https://a.test"
	want := []string{filepath.Join(dir, "rundll32.exe"), "url.dll,FileProtocolHandler", url}
	if got := openCommand(url); !slices.Equal(got, want) {
		t.Errorf("openCommand(%q) = %q, want %q", url, got, want)
	}
}
