package cli

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachAttrs starts the core with no console, in a process group of its
// own, so that Ctrl-C and closing the console it was started from do not
// reach it, and out of the job the launching shell may be in (Windows
// Terminal, a CI runner), which would end it with the shell.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NO_WINDOW,
	}
}

// startDetached starts the command newCmd makes with detachAttrs. A job that
// forbids breakaway makes CreateProcess refuse with access denied; the core
// is then started inside it, which the core log notes, as it ends with
// whatever ends the job. The kill-on-close job the core puts its ssh.exe
// children in is its own, made after it starts, so it is not inherited.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	attrs := detachAttrs()
	c := newCmd()
	c.SysProcAttr = attrs
	err := c.Start()
	if err == nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return c, err
	}
	attrs.CreationFlags &^= windows.CREATE_BREAKAWAY_FROM_JOB
	c = newCmd()
	c.SysProcAttr = attrs
	if err := c.Start(); err != nil {
		return c, err
	}
	noteInCoreLog("The job nearhost -d was started in forbids leaving it, so this core stays in it and stops when that job ends.")
	return c, nil
}
