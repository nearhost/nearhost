package e2e

import "os/exec"

// ownTerminal does nothing: a ConPTY is the program's console already.
func ownTerminal(*exec.Cmd) {}
