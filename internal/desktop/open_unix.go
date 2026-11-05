//go:build unix && !darwin

package desktop

// openCommand is the command, with its arguments, that opens url.
func openCommand(url string) []string { return []string{"xdg-open", url} }
