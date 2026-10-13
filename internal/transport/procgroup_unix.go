//go:build unix

package transport

import (
	"os/exec"
	"syscall"
)

// ownGroup makes cmd start in a new process group. Signals a terminal sends
// to nearhost's group (Ctrl-C) then do not reach it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
