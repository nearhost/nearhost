package tui

import "github.com/nearhost/nearhost/internal/winsys"

// systemClipboard copies text with clip.exe, as UTF-16 so that text outside
// the console's code page arrives intact. Tests replace it, so they leave the
// clipboard alone.
var systemClipboard = func(text string) error {
	return copyWithInput([][]string{{clipTool()}}, utf16LE(text))
}

// clipTool is clip.exe in the system directory.
func clipTool() string { return winsys.Tool("clip.exe") }
