package transport

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
	"golang.org/x/sys/windows"
)

// jobHelperEnv names the fake ssh for TestJobHelperProcess, and is set only
// when the test binary runs as that helper.
const jobHelperEnv = "NEARHOST_TEST_JOB_HELPER_SSH"

// TestJobHelperProcess is not a test: run as a child of
// TestKilledNearhostTakesItsSSHWithItOnWindows, it stands in for nearhost.
// It starts a per-process link with the fake ssh, prints the ssh's pid and
// waits to be killed.
func TestJobHelperProcess(t *testing.T) {
	sshPath := os.Getenv(jobHelperEnv)
	if sshPath == "" {
		t.Skip("run as a helper process only")
	}
	p := newPerProcess(Config{Name: "mini", Host: "mini", SSHPath: sshPath, Env: []string{"FAKE_SSH_OUTLIVE_PARENT=1"}})
	p.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	proc, err := p.startProc(p.args("-N"), newSSHStderr("", ""))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("pid %d\n", proc.cmd.Process.Pid)
	select {}
}

func TestKilledNearhostTakesItsSSHWithItOnWindows(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=^TestJobHelperProcess$")
	helper.Env = append(os.Environ(), jobHelperEnv+"="+testutil.FakeSSH(t))
	helper.Stderr = os.Stderr
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		helper.Process.Kill()
		helper.Wait()
	})

	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read the ssh's pid from the helper: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(line), "pid "))
	if err != nil {
		t.Fatalf("helper said %q, want its ssh's pid", line)
	}
	ssh, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open the ssh (pid %d): %v", pid, err)
	}
	defer windows.CloseHandle(ssh)

	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	ev, err := windows.WaitForSingleObject(ssh, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if ev != windows.WAIT_OBJECT_0 {
		windows.TerminateProcess(ssh, 1)
		t.Fatalf("the ssh (pid %d) outlived its killed parent by 5 s", pid)
	}
}
