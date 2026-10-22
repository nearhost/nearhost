package desktop

import "github.com/nearhost/nearhost/internal/winsys"

// openCommand is the command, with its arguments, that opens url. `start`
// would need cmd.exe, which reads & and ^ in the URL as its own syntax.
func openCommand(url string) []string {
	return []string{winsys.Tool("rundll32.exe"), "url.dll,FileProtocolHandler", url}
}
