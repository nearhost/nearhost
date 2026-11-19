//go:build !unix && !windows

package transport

import "os/exec"

func ownGroup(*exec.Cmd) {}

// joinJob does nothing: this system has no job objects.
func joinJob(*exec.Cmd) error { return nil }
