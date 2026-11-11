package testutil

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
