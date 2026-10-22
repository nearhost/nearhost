package tui

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// realClipboard is systemClipboard before fake_test.go's init replaces it:
// package variables are set before any init runs.
var realClipboard = systemClipboard

func TestSystemClipboardKeepsUnicodeOnWindows(t *testing.T) {
	text := "https://café.test/😀"
	if err := realClipboard(text); err != nil {
		t.Fatal(err)
	}
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	powershell := filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
	// Get-Clipboard writes through the console's code page; UTF-8 output
	// keeps what it read intact. [Text.Encoding]::UTF8 would put a byte
	// order mark before it.
	script := "[Console]::OutputEncoding = New-Object Text.UTF8Encoding $false; Get-Clipboard -Raw"
	out, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimRight(string(out), "\r\n"); got != text {
		t.Fatalf("clipboard holds %q, want %q", got, text)
	}
}

func TestSystemClipboardUsesSystem32OnWindows(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := clipTool(), filepath.Join(dir, "clip.exe"); got != want {
		t.Fatalf("clipTool() = %q, want %q", got, want)
	}
}
