package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// pgid returns 0: Windows has no process groups to record in
// $FAKE_SSH_PGID_LOG.
func pgid() int {
	return 0
}

// execServer runs the SFTP server argv on this process's standard streams
// and returns its exit status. Windows cannot replace a process, so the
// server is a child, and killing the fake ssh leaves it running until its
// input closes.
func execServer(argv []string) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "fake ssh: %s: %v\n", argv[0], err)
	return 255
}
