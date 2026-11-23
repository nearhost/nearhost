//go:build windows

package service

import (
	"os/exec"
	"syscall"
)

// createNoWindow stops a console program from opening a console window.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
