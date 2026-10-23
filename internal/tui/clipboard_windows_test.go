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

// readClipboard returns the clipboard's text, read by PowerShell.
func readClipboard(t *testing.T) string {
	t.Helper()
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
	// Write-Output ends what it writes with a line break of its own.
	return strings.TrimSuffix(string(out), "\r\n")
}

// keepClipboard puts the clipboard's text back when the test ends, so a run
// on a developer's machine leaves what they had copied.
func keepClipboard(t *testing.T) {
	saved := readClipboard(t)
	t.Cleanup(func() { realClipboard(saved) })
}

func TestSystemClipboardKeepsUnicodeOnWindows(t *testing.T) {
	keepClipboard(t)
	text := "https://café.test/😀"
	if err := realClipboard(text); err != nil {
		t.Fatal(err)
	}
	if got := readClipboard(t); got != text {
		t.Fatalf("clipboard holds %q, want %q", got, text)
	}
}

func TestSystemClipboardEndsLinesWithCRLFOnWindows(t *testing.T) {
	keepClipboard(t)
	if err := realClipboard("one\ntwo\r\nthree"); err != nil {
		t.Fatal(err)
	}
	if got, want := readClipboard(t), "one\r\ntwo\r\nthree"; got != want {
		t.Fatalf("clipboard holds %q, want %q", got, want)
	}
}
