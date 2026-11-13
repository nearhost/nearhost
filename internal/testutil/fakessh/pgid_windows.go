package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pgid returns 0: Windows has no process groups to record in
// $FAKE_SSH_PGID_LOG.
func pgid() int {
	return 0
}

// execServer runs the SFTP server argv on this process's standard streams
// and returns its exit status. Windows cannot replace a process, so the
// server is a child. To end with this process, as a real ssh's streams do,
// this process first joins a job that kills what is left in it when its
// last handle closes: the handle is this process's own, so a kill of the
// fake ssh ends the server too and closes the streams it shares. Without
// that, the server would hold them open until its input closed.
func execServer(argv []string) int {
	if err := killChildrenOnExit(); err != nil {
		fmt.Fprintf(os.Stderr, "fake ssh: job object: %v\n", err)
		return 255
	}
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

// killChildrenOnExit puts this process in a new job object that kills its
// processes when the job's last handle closes, and keeps the one handle,
// which is not inherited, for as long as this process lives. Children
// started after it join the job.
func killChildrenOnExit() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return err
	}
	return nil
}
