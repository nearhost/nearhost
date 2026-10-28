package desktop

// openCommand is the command, with its arguments, that opens url. `start`
// would need cmd.exe, which reads & and ^ in the URL as its own syntax.
func openCommand(url string) []string {
	return []string{"rundll32", "url.dll,FileProtocolHandler", url}
}
