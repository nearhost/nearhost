package winsys

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// job is the process's one kill-on-close job object, made on first use.
var job struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

// KillOnCloseJob returns a job object that ends every process in it when
// its last handle closes. It is made once per process, and its one handle,
// which children do not inherit, stays open until this process ends, so
// the processes in the job end with it, however it ends.
func KillOnCloseJob() (windows.Handle, error) {
	job.once.Do(func() {
		job.handle, job.err = newKillOnCloseJob()
	})
	return job.handle, job.err
}

// newKillOnCloseJob creates a job object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE set.
func newKillOnCloseJob() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

// AssignToJob puts the started process with the given pid in the job
// KillOnCloseJob returns, so it ends when this process does. The processes
// it starts afterwards join the job too.
func AssignToJob(pid int) error {
	h, err := KillOnCloseJob()
	if err != nil {
		return err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(proc)
	return windows.AssignProcessToJobObject(h, proc)
}
