package transport

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ownGroup makes cmd start in a new process group with no console window.
// Ctrl-C at nearhost's console then does not reach it, and ssh does not open
// a console of its own.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}
