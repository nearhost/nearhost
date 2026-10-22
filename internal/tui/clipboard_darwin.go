package tui

// systemClipboard copies text with pbcopy. Tests replace it, so they leave
// the clipboard alone.
var systemClipboard = func(text string) error {
	return copyWith([][]string{{"pbcopy"}}, text)
}
