//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detachAttrs puts the core in a session of its own, with no controlling
// terminal, so that closing the terminal it was started from, and the hangup
// that sends, does not reach it.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// startDetached starts the command newCmd makes with detachAttrs.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	c := newCmd()
	c.SysProcAttr = detachAttrs()
	return c, c.Start()
}
