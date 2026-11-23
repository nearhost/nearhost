//go:build !windows

package service

import "os/exec"

// HideWindow stops cmd from opening a console window on Windows. It does
// nothing elsewhere.
func HideWindow(*exec.Cmd) {}
