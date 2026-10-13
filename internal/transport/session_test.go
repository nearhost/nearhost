package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newFakeSession returns a Session wired to the fake ssh script.
func newFakeSession(t *testing.T, env ...string) (*Session, string) {
	t.Helper()
	ssh, err := filepath.Abs("testdata/fakessh/ssh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ssh, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	s := NewSession("mini", "mini", dir)
	s.SSHPath = ssh
	s.Env = append([]string{"FAKE_SSH_LOG=" + logPath}, env...)
	s.pollInterval = 20 * time.Millisecond
	s.connectTimeout = 2 * time.Second
	s.baseBackoff = 10 * time.Millisecond
	s.maxBackoff = 50 * time.Millisecond
	t.Cleanup(s.Stop)
	return s, logPath
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func TestCheckSucceedsAndUsesBatchMode(t *testing.T) {
	s, logPath := newFakeSession(t)
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	log := readLog(t, logPath)
	if !strings.Contains(log, "-o BatchMode=yes") || !strings.HasSuffix(strings.TrimSpace(log), "mini true") {
		t.Fatalf("unexpected ssh invocation: %q", log)
	}
}

func TestCheckReturnsStderrOnFailure(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_AUTH_FAIL=1")
	err := s.Check(context.Background())
	var ce *CheckError
	if !errors.As(err, &ce) || !strings.Contains(ce.Stderr, "Permission denied") {
		t.Fatalf("expected CheckError with stderr, got %v", err)
	}
}

func TestStartConnectsAndStopExitsMaster(t *testing.T) {
	s, logPath := newFakeSession(t)
	s.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.WaitConnected(ctx); err != nil {
		t.Fatalf("WaitConnected: %v (last error %q)", err, s.LastError())
	}
	if s.Status() != StatusConnected {
		t.Fatalf("status = %s", s.Status())
	}
	if !strings.Contains(readLog(t, logPath), "-N -M -S "+s.SocketPath()) {
		t.Fatalf("master not launched with control socket: %q", readLog(t, logPath))
	}
	s.Stop()
	if s.Status() != StatusDisconnected {
		t.Fatalf("status after stop = %s", s.Status())
	}
	if !strings.Contains(readLog(t, logPath), "-O exit") {
		t.Fatal("expected -O exit on stop")
	}
}

func TestSupervisorReconnectsAfterMasterExit(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_FLAKY_ONCE=0.3")
	s.Start()
	waitFor(t, 5*time.Second, func() bool { return s.Launches() >= 2 && s.Status() == StatusConnected }, "reconnect")
}

func TestSupervisorStopsAfterRepeatedAuthFailures(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_AUTH_FAIL=1")
	s.Start()
	waitFor(t, 5*time.Second, func() bool { return s.Status() == StatusAuthFailed }, "auth failed status")
	if s.Launches() != 3 {
		t.Fatalf("expected 3 launches, got %d", s.Launches())
	}
	if !strings.Contains(s.LastError(), "Permission denied") {
		t.Fatalf("LastError = %q", s.LastError())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.WaitConnected(ctx); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("WaitConnected should surface the auth error, got %v", err)
	}
}

func TestHostKeyFailureIsFatal(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_HOSTKEY_FAIL=1")
	s.Start()
	waitFor(t, 5*time.Second, func() bool { return s.Status() == StatusAuthFailed }, "auth failed status")
	if s.Launches() != 3 || !strings.Contains(s.LastError(), "Host key verification failed") {
		t.Fatalf("launches=%d lastErr=%q", s.Launches(), s.LastError())
	}
}

func TestStopIsIdempotentBeforeStart(t *testing.T) {
	s, _ := newFakeSession(t)
	s.Stop()
	s.Stop()
	if s.Status() != StatusDisconnected {
		t.Fatalf("status = %s", s.Status())
	}
}

func TestStartThenImmediateStopDoesNotPanic(t *testing.T) {
	for i := 0; i < 20; i++ {
		s, _ := newFakeSession(t)
		s.Start()
		s.Stop()
		if s.Status() != StatusDisconnected {
			t.Fatalf("iteration %d: status = %s", i, s.Status())
		}
		s.Stop()
	}
}

func TestStartStopStartAgain(t *testing.T) {
	s, _ := newFakeSession(t)
	s.Start()
	s.Stop()
	s.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.WaitConnected(ctx); err != nil {
		t.Fatalf("WaitConnected after restart: %v", err)
	}
}

func TestIsFatalSSHError(t *testing.T) {
	fatal := []string{
		"user@host: Permission denied (publickey).",
		"Host key verification failed.",
		"Too many authentication failures",
	}
	for _, m := range fatal {
		if !isFatalSSHError(m) {
			t.Errorf("%q should be fatal", m)
		}
	}
	retry := []string{
		"Warning: Identity file /x/id_missing not accessible: No such file or directory.",
		"client_loop: send disconnect: Broken pipe",
		"ssh exited",
	}
	for _, m := range retry {
		if isFatalSSHError(m) {
			t.Errorf("%q should be retried", m)
		}
	}
}

func TestConnectTimeoutKillsStuckMasterAndRetries(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_HANG=1")
	s.connectTimeout = 150 * time.Millisecond
	s.Start()
	waitFor(t, 5*time.Second, func() bool { return s.Launches() >= 2 }, "second launch after connect timeout")
	if s.Status() == StatusConnected {
		t.Fatalf("status = %s", s.Status())
	}
}

func TestStartDuringStopWaitsForOldSupervisor(t *testing.T) {
	s, _ := newFakeSession(t)
	var mu sync.Mutex
	var events []string
	s.Logf = func(format string, args ...any) {
		mu.Lock()
		events = append(events, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	s.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	// Start as soon as Stop has detached the running supervisor.
	waitFor(t, 2*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cancel == nil
	}, "stop to begin")
	s.Start()
	<-stopped
	if err := s.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	s.Stop()

	mu.Lock()
	defer mu.Unlock()
	disc, conn := -1, -1
	for i, e := range events {
		if e == "mini: disconnected" && disc < 0 {
			disc = i
		}
		if e == "mini: connecting" && i > 0 && conn < 0 && disc < 0 {
			conn = i // a second "connecting" before the first disconnect
		}
	}
	if conn >= 0 {
		t.Fatalf("new supervisor started before old one exited: %v", events)
	}
}
