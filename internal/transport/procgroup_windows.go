package transport

import (
	"os/exec"
	"syscall"

	"github.com/nearhost/nearhost/internal/winsys"
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

// joinJob puts cmd, which has started, in the job that ends its processes
// when nearhost ends. Windows has no process tree to end with nearhost, so
// without it an ssh.exe outlives a nearhost that is killed or crashes, and
// keeps its forwards and ports. What cmd starts from here on joins the job
// too.
func joinJob(cmd *exec.Cmd) error {
	return winsys.AssignToJob(cmd.Process.Pid)
}
