//go:build !unix && !windows

package transport

import "os/exec"

func ownGroup(*exec.Cmd) {}
