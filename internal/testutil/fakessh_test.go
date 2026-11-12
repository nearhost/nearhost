package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// runFake runs the fake ssh with args and the extra environment env, and
// returns its exit code and standard error.
func runFake(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(FakeSSH(t), args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), stderr.String()
	}
	t.Fatalf("run the fake ssh: %v", err)
	return 0, ""
}

// TestFakeSSHRunsAMaster starts a master, checks it and asks it to exit. On
// Windows a master that is killed rather than asked cannot remove its socket;
// this test asks, so it holds there too.
func TestFakeSSHRunsAMaster(t *testing.T) {
	dir := TempDir(t, "nh-")
	sock := filepath.Join(dir, "s")
	log := filepath.Join(dir, "ssh.log")
	env := []string{"FAKE_SSH_LOG=" + log}

	master := exec.Command(FakeSSH(t), "-N", "-M", "-S", sock, "mini")
	master.Env = append(os.Environ(), env...)
	if err := master.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- master.Wait() }()
	t.Cleanup(func() {
		master.Process.Kill()
		<-done
	})
	WaitUntil(t, 2*time.Second, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	}, "the master's socket")

	if code, stderr := runFake(t, env, "-O", "check", "-S", sock, "mini"); code != 0 {
		t.Fatalf("check = %d %q", code, stderr)
	}
	if code, stderr := runFake(t, env, "-O", "exit", "-S", sock, "mini"); code != 0 {
		t.Fatalf("exit = %d %q", code, stderr)
	}
	select {
	case err := <-done:
		done <- err
		if err != nil {
			t.Fatalf("the master ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the master did not exit")
	}

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "-N -M -S "+sock+" mini" {
		t.Fatalf("log =\n%s", b)
	}
}

// TestFakeSSHFailsAuth fails a master's authentication as a server that
// refuses the key does.
func TestFakeSSHFailsAuth(t *testing.T) {
	sock := filepath.Join(TempDir(t, "nh-"), "s")
	code, stderr := runFake(t, []string{"FAKE_SSH_AUTH_FAIL=1"}, "-N", "-M", "-S", sock, "mini")
	if code != 255 || stderr != "user@host: Permission denied (publickey).\n" {
		t.Fatalf("master = %d %q", code, stderr)
	}
}

// TestFakeSSHFailsOneForward fails the forward of the one local port that
// FAKE_SSH_FORWARD_FAIL names and lets the others through.
func TestFakeSSHFailsOneForward(t *testing.T) {
	env := []string{"FAKE_SSH_FORWARD_FAIL=4100"}
	code, stderr := runFake(t, env, "-O", "forward", "-L", "127.0.0.1:4100:localhost:80", "-S", "s", "mini")
	if code != 255 || !strings.Contains(stderr, "bind [127.0.0.1]:4100: Address already in use") {
		t.Fatalf("forward of 4100 = %d %q", code, stderr)
	}
	if code, stderr := runFake(t, env, "-O", "forward", "-L", "127.0.0.1:4101:localhost:80", "-S", "s", "mini"); code != 0 {
		t.Fatalf("forward of 4101 = %d %q", code, stderr)
	}
}

// TestFakeSSHBuildsOnceInParallel asks for the fake from several goroutines
// at once: all get the same program, and it runs. The goroutines call
// buildOnce, which returns its error, because only the test goroutine may
// fail the test.
func TestFakeSSHBuildsOnceInParallel(t *testing.T) {
	paths := make([]string, 8)
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = buildOnce("github.com/nearhost/nearhost/internal/testutil/fakessh", "ssh")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if FakeSSH(t) != paths[0] {
		t.Fatalf("FakeSSH = %q, want %q", FakeSSH(t), paths[0])
	}
	for _, p := range paths {
		if p != paths[0] || p == "" {
			t.Fatalf("paths = %v", paths)
		}
	}
	missing := filepath.Join(TempDir(t, "nh-"), "s")
	if code, stderr := runFake(t, nil, "-O", "check", "-S", missing, "mini"); code != 255 {
		t.Fatalf("check of a missing socket = %d %q", code, stderr)
	}
}

// TestFakeToolDoesWhatItIsTold checks both instructions the fake tool
// understands: exiting with a code, and copying its standard input to a file.
func TestFakeToolDoesWhatItIsTold(t *testing.T) {
	dir := TempDir(t, "nh-")
	FakeTool(t, dir, "failing", "exit 3")
	err := exec.Command(filepath.Join(dir, "failing")).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("exit tool: err = %v, want exit code 3", err)
	}

	out := filepath.Join(dir, "copied")
	FakeTool(t, dir, "copying", "copy "+out)
	cmd := exec.Command(filepath.Join(dir, "copying"))
	cmd.Stdin = strings.NewReader("ssh mini")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "ssh mini" {
		t.Errorf("copy tool wrote %q, %v", b, err)
	}
}

// plainFake is a running plain ssh -N: its pid and a way to read its stderr
// and wait for it.
type plainFake struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
	done   chan struct{}
}

// syncBuffer is a bytes.Buffer that the test and the process's copier can
// use at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startPlain starts the fake ssh with args and env and kills it when the
// test ends.
func startPlain(t *testing.T, env []string, args ...string) *plainFake {
	t.Helper()
	p := &plainFake{
		cmd:    exec.Command(FakeSSH(t), args...),
		stderr: &syncBuffer{},
		done:   make(chan struct{}),
	}
	p.cmd.Env = append(os.Environ(), env...)
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { p.cmd.Process.Kill(); <-p.done })
	return p
}

func TestFakeSSHPlainForwardListensAndSaysSo(t *testing.T) {
	port := FreePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	p := startPlain(t, nil, "-N", "-o", "LogLevel=DEBUG1", "-L", addr+":localhost:80", "mini")
	want := "Local forwarding listening on 127.0.0.1 port " + strconv.Itoa(port) + "."
	WaitUntil(t, 3*time.Second, func() bool { return strings.Contains(p.stderr.String(), want) }, "the forward to listen")
	got := p.stderr.String()
	if a, l := strings.Index(got, "Authenticated to mini"), strings.Index(got, want); a < 0 || a > l {
		t.Errorf("stderr should say Authenticated to mini before the listening line:\n%s", got)
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("the forwarded port should accept a connection: %v", err)
	}
	conn.Close()

	p.cmd.Process.Kill()
	<-p.done
	WaitUntil(t, 2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return false
		}
		return true
	}, "the forwarded port to close")
}

func TestFakeSSHPlainForwardOnATakenPortExits(t *testing.T) {
	port := FreePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	code, stderr := runFake(t, nil, "-N", "-o", "ExitOnForwardFailure=yes", "-L", addr+":localhost:80", "mini")
	if code != 255 {
		t.Errorf("exit code = %d, want 255", code)
	}
	if want := fmt.Sprintf("bind [127.0.0.1]:%d: Address already in use", port); !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestFakeSSHPlainRemoteForward(t *testing.T) {
	p := startPlain(t, nil, "-N", "-R", "/x/a.sock:/y/b.sock", "mini")
	WaitUntil(t, 3*time.Second, func() bool {
		return strings.Contains(p.stderr.String(), "remote forward success for: listen /x/a.sock")
	}, "the remote forward to succeed")

	code, stderr := runFake(t, []string{"FAKE_SSH_RFORWARD_FAIL=refused"}, "-N", "-R", "/x/a.sock:/y/b.sock", "mini")
	if code != 255 {
		t.Errorf("exit code = %d, want 255", code)
	}
	if want := "Error: remote port forwarding failed for listen path /x/a.sock"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestFakeSSHDropsWhenAsked(t *testing.T) {
	dir := TempDir(t, "nh-")
	pidLog := filepath.Join(dir, "pids")
	drops := filepath.Join(dir, "drops")
	if err := os.Mkdir(drops, 0o755); err != nil {
		t.Fatal(err)
	}
	p := startPlain(t, []string{"FAKE_SSH_PID_LOG=" + pidLog, "FAKE_SSH_DROP_DIR=" + drops}, "-N", "mini")
	var pid string
	WaitUntil(t, 3*time.Second, func() bool {
		b, _ := os.ReadFile(pidLog)
		f := strings.Fields(string(b))
		if len(f) == 0 {
			return false
		}
		pid = f[0]
		return true
	}, "the fake to log its pid")
	if want := strconv.Itoa(p.cmd.Process.Pid); pid != want {
		t.Errorf("logged pid = %s, want %s", pid, want)
	}
	if err := os.WriteFile(filepath.Join(drops, pid), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the fake should exit within 2s of its drop file")
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 255 {
		t.Errorf("exit code = %d, want 255", code)
	}
	if !strings.Contains(p.stderr.String(), "Broken pipe") {
		t.Errorf("stderr = %q, want Broken pipe", p.stderr.String())
	}
}

func TestFakeSSHPrintsConfig(t *testing.T) {
	cmd := exec.Command(FakeSSH(t), "-G", "mini")
	cmd.Env = append(os.Environ(), "FAKE_SSH_G_CONFIG=user ecs\nport 2222")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	want := []string{"hostname mini", "user ecs", "port 2222"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("-G output = %q, want %q", lines, want)
	}
}
