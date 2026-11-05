//go:build unix && !darwin

package tui

// systemClipboard copies text with the first of wl-copy, xclip and xsel that
// works. Tests replace it, so they leave the clipboard alone.
var systemClipboard = func(text string) error {
	return copyWith([][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}, text)
}
