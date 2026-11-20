//go:build !windows

package e2e

import (
	"os/exec"
	"syscall"
)

// ownTerminal makes the pseudo-terminal the program's controlling terminal,
// in a session of its own, as a shell's terminal is. Without it the program
// is sent no SIGWINCH when the terminal is resized, and no signal for a key
// such as ctrl+c.
func ownTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
}
