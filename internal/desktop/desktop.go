// Package desktop hands things to the desktop Nearhost runs on, such as the
// browser.
package desktop

import (
	"os/exec"
	"time"
)

// openGrace is how long OpenURL waits for an opener that exits early, so a
// quick failure still reaches the caller as an error.
const openGrace = 500 * time.Millisecond

// opener builds the command that opens a URL. Tests replace it.
var opener = openCommand

// standIn, when set, opens URLs in the opener's place. Only the e2e build
// sets it (e2e.go), so a journey never opens a browser.
var standIn func(url string) error

// OpenURL opens url with the system's opener and returns once the opener has
// started. Some openers, such as xdg-open without a desktop environment, run
// the browser in the foreground until it closes, so OpenURL does not wait for
// the opener to exit. It does wait up to openGrace: an opener that fails to
// start, or exits non-zero within that time, returns an error. The opener's
// output is discarded so it cannot disturb a full-screen terminal.
func OpenURL(url string) error {
	if standIn != nil {
		return standIn(url)
	}
	args := opener(url)
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(openGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return nil
	}
}
