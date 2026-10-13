//go:build !unix

package transport

import "os/exec"

func ownGroup(*exec.Cmd) {}
