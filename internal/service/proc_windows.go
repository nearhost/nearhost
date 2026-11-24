//go:build windows

package service

import (
	"os/exec"
	"syscall"
)

// createNoWindow stops a console program from opening a console window.
const createNoWindow = 0x08000000

// HideWindow stops cmd from opening a console window on Windows. It does
// nothing elsewhere.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
