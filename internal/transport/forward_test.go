package transport

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func connected(t *testing.T, s *Session) {
	t.Helper()
	s.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.WaitConnected(ctx); err != nil {
		t.Fatalf("WaitConnected: %v", err)
	}
}

func TestAddForwardRunsControlCommand(t *testing.T) {
	s, logPath := newFakeSession(t)
	connected(t, s)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, logPath), "-O forward -L 127.0.0.1:8080:localhost:80 mini") {
		t.Fatalf("forward not issued: %q", readLog(t, logPath))
	}
	if f := s.Forwards(); len(f) != 1 || f[0] != [2]int{8080, 80} {
		t.Fatalf("Forwards = %v", f)
	}
	if err := s.CancelForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, logPath), "-O cancel -L 127.0.0.1:8080:localhost:80 mini") {
		t.Fatal("cancel not issued")
	}
	if len(s.Forwards()) != 0 {
		t.Fatal("forward should be forgotten after cancel")
	}
}

func TestAddForwardReportsBindFailure(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_FORWARD_FAIL=5174")
	connected(t, s)
	err := s.AddForward(context.Background(), 5174, 5174)
	if err == nil || !strings.Contains(err.Error(), "Address already in use") {
		t.Fatalf("expected bind error, got %v", err)
	}
	if len(s.Forwards()) != 0 {
		t.Fatal("failed forward must not be remembered")
	}
}

func TestForwardsAreReappliedAfterReconnect(t *testing.T) {
	s, logPath := newFakeSession(t)
	const spec = "-O forward -L 127.0.0.1:8080:localhost:80"
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatalf("queueing a forward before connect should succeed: %v", err)
	}
	s.Start()
	waitFor(t, 5*time.Second, func() bool {
		return s.Status() == StatusConnected && strings.Count(readLog(t, logPath), spec) >= 1
	}, "forward issued on the first master")
	if s.Launches() != 1 {
		t.Fatalf("Launches = %d before the drop, want 1", s.Launches())
	}
	// The fake master exits once its socket file disappears: drop it on demand.
	if err := os.Remove(s.SocketPath()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return s.Launches() >= 2 && s.Status() == StatusConnected &&
			strings.Count(readLog(t, logPath), spec) >= 2
	}, "forward reapplied after reconnect")
}

func TestForwardAddedWhileConnectedIsNotIssuedTwice(t *testing.T) {
	s, logPath := newFakeSession(t)
	connected(t, s)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	s.reapplyForwards(context.Background()) // same connection: must be a no-op
	if n := strings.Count(readLog(t, logPath), "-O forward -L 127.0.0.1:8080:localhost:80"); n != 1 {
		t.Fatalf("forward issued %d times on one connection, want 1", n)
	}
}

func TestAddForwardTwiceKeepsForwardTracked(t *testing.T) {
	s, logPath := newFakeSession(t)
	connected(t, s)
	for i := 0; i < 2; i++ {
		if err := s.AddForward(context.Background(), 8080, 80); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(readLog(t, logPath), "-O forward -L 127.0.0.1:8080:localhost:80"); n != 1 {
		t.Fatalf("duplicate add re-issued the forward %d times", n)
	}
	if err := s.CancelForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, logPath), "-O cancel -L 127.0.0.1:8080:localhost:80") {
		t.Fatal("forward no longer cancellable")
	}
}

func TestFailedReAddRestoresTrackedForward(t *testing.T) {
	s, _ := newFakeSession(t, "FAKE_SSH_FORWARD_FAIL=5174")
	// Queued while disconnected, so tracked but not yet applied.
	if err := s.AddForward(context.Background(), 5174, 5174); err != nil {
		t.Fatal(err)
	}
	connectedNoReapply := func() {
		s.mu.Lock()
		s.status = StatusConnected
		s.launches = 1
		s.mu.Unlock()
	}
	connectedNoReapply()
	if err := s.AddForward(context.Background(), 5174, 5174); err == nil {
		t.Fatal("expected bind error")
	}
	if len(s.Forwards()) != 1 {
		t.Fatalf("pre-existing forward dropped after failed re-add: %v", s.Forwards())
	}
}

func TestFreePortIsBindable(t *testing.T) {
	p, err := FreePort()
	if err != nil || p < 1024 || p > 65535 {
		t.Fatalf("FreePort = %d, %v", p, err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		t.Fatalf("port %d not bindable: %v", p, err)
	}
	l.Close()
}

func TestFailedCancelKeepsForwardTrackedForRetry(t *testing.T) {
	flag := t.TempDir() + "/cancel-fails"
	s, logPath := newFakeSession(t, "FAKE_SSH_CANCEL_FAIL="+flag)
	connected(t, s)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelForward(context.Background(), 8080, 80); err == nil {
		t.Fatal("expected the cancel to fail")
	}
	if f := s.Forwards(); len(f) != 1 || f[0] != [2]int{8080, 80} {
		t.Fatalf("forward still active on the master must stay tracked, got %v", f)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelForward(context.Background(), 8080, 80); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(s.Forwards()) != 0 {
		t.Fatal("forward should be forgotten once the cancel succeeds")
	}
	if n := strings.Count(readLog(t, logPath), "-O cancel -L 127.0.0.1:8080:localhost:80 mini"); n != 2 {
		t.Fatalf("expected the retry to reach ssh, saw %d cancel commands", n)
	}
}

func TestCancelForwardWhileDisconnectedForgetsWithoutSSH(t *testing.T) {
	s, logPath := newFakeSession(t)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if len(s.Forwards()) != 0 || strings.Contains(readLog(t, logPath), "-O cancel") {
		t.Fatalf("forwards=%v log=%q", s.Forwards(), readLog(t, logPath))
	}
}
