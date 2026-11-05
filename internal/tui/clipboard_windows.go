package tui

// systemClipboard copies text with clip.exe, as UTF-16 so that text outside
// the console's code page arrives intact. Tests replace it, so they leave the
// clipboard alone.
var systemClipboard = func(text string) error {
	return copyWithInput([][]string{{"clip.exe"}}, utf16LE(text))
}
