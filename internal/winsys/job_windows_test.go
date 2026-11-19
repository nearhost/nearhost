package winsys

import (
	"os/exec"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isProcessInJob is kernel32's IsProcessInJob, which x/sys/windows lacks.
var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func TestKillOnCloseJobIsMadeOnce(t *testing.T) {
	first, err := KillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	second, err := KillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("KillOnCloseJob returned %v, then %v", first, second)
	}
}

func TestAssignToJobPutsTheProcessInTheJob(t *testing.T) {
	cmd := exec.Command(Tool("cmd.exe"), "/c", "pause")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})
	if err := AssignToJob(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	job, err := KillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(proc)
	var in int32
	if r, _, err := isProcessInJob.Call(uintptr(proc), uintptr(job), uintptr(unsafe.Pointer(&in))); r == 0 {
		t.Fatal(err)
	}
	if in == 0 {
		t.Fatal("the process is not in the job")
	}
}
