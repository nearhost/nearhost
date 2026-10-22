// Package desktop hands things to the desktop Nearhost runs on, such as the
// browser.
package desktop

import "os/exec"

// OpenURL opens url with the system's opener.
func OpenURL(url string) error {
	args := openCommand(url)
	return exec.Command(args[0], args[1:]...).Run()
}
