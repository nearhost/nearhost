//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// pgid returns this process's process group, which $FAKE_SSH_PGID_LOG
// records.
func pgid() int {
	return syscall.Getpgrp()
}

// execServer replaces this process with the SFTP server argv, as the shell's
// exec did, so the server keeps this process's id and a kill of the fake ssh
// ends the server and closes its streams. It returns only on failure.
func execServer(argv []string) int {
	err := syscall.Exec(argv[0], argv, os.Environ())
	fmt.Fprintf(os.Stderr, "fake ssh: %s: %v\n", argv[0], err)
	return 255
}
