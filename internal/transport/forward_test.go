package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
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
	if !strings.Contains(readLog(t, logPath), "-O forward -L 127.0.0.1:8080:localhost:80 -- mini") {
		t.Fatalf("forward not issued: %q", readLog(t, logPath))
	}
	if f := s.Forwards(); len(f) != 1 || f[0] != [2]int{8080, 80} {
		t.Fatalf("Forwards = %v", f)
	}
	if err := s.CancelForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, logPath), "-O cancel -L 127.0.0.1:8080:localhost:80 -- mini") {
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
	if n := strings.Count(readLog(t, logPath), "-O cancel -L 127.0.0.1:8080:localhost:80 -- mini"); n != 2 {
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

// applied reports the Applied flag of the single tracked forward 8080:80.
func applied(t *testing.T, s *Session) bool {
	t.Helper()
	status, st := s.ForwardStates()
	if len(st) != 1 || st[0].Local != 8080 || st[0].Remote != 80 {
		t.Fatalf("ForwardStates = %+v, want the one forward 8080:80", st)
	}
	if st[0].Applied && status != StatusConnected {
		t.Fatalf("forward reported applied with status %s", status)
	}
	return st[0].Applied
}

func TestForwardStatesAppliedAfterAdd(t *testing.T) {
	s, _ := newFakeSession(t)
	connected(t, s)
	if status, st := s.ForwardStates(); status != StatusConnected || len(st) != 0 {
		t.Fatalf("ForwardStates = %s %+v before any forward", status, st)
	}
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if !applied(t, s) {
		t.Fatal("a forward added on a connected session must be applied")
	}
	s.Stop()
	if applied(t, s) {
		t.Fatal("no forward is applied on a stopped session")
	}
	if status, _ := s.ForwardStates(); status != StatusDisconnected {
		t.Fatalf("status in the snapshot = %s after Stop", status)
	}
}

func TestForwardStatesNotAppliedWhileDisconnected(t *testing.T) {
	s, _ := newFakeSession(t)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	if applied(t, s) {
		t.Fatal("a forward queued while disconnected is tracked, not applied")
	}
	connected(t, s)
	if !applied(t, s) {
		t.Fatal("WaitConnected returned before the queued forward was applied")
	}
}

func TestForwardStatesFollowReapplyAcrossReconnects(t *testing.T) {
	flag := t.TempDir() + "/forward-fails"
	s, _ := newFakeSession(t, "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
	connected(t, s)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	reconnect := func() {
		t.Helper()
		before := s.Launches()
		if err := os.Remove(s.SocketPath()); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool { return s.Launches() > before && s.Status() == StatusConnected }, "reconnect")
	}

	// The re-apply fails on the new master: tracked, connected, not applied.
	if err := os.WriteFile(flag, []byte("8080"), 0o600); err != nil {
		t.Fatal(err)
	}
	reconnect()
	// Connected is only reported once the re-apply pass is over, so there is
	// nothing to wait for here.
	if applied(t, s) {
		t.Fatal("a forward whose re-apply failed must not be reported applied")
	}

	// A later re-apply succeeds.
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	reconnect()
	if !applied(t, s) {
		t.Fatal("the forward must be applied again once a re-apply succeeds")
	}
}

func TestMasterDyingDuringReapplyIsNotReportedConnected(t *testing.T) {
	flag := t.TempDir() + "/forward-kills-master"
	s, logPath := newFakeSession(t, "FAKE_SSH_FORWARD_KILLS_MASTER="+flag)
	var mu sync.Mutex
	var lines []string
	s.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	connectedLines := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if l == "mini: connected" {
				n++
			}
		}
		return n
	}
	connected(t, s)
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	// The connection drops, and the next master dies while the forward is
	// being re-applied on it.
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.SocketPath()); err != nil {
		t.Fatal(err)
	}
	// The status changes before the supervisor logs it, so the status alone
	// does not say the line is there: wait for the supervisor to log the
	// third master (the third "connecting") as connected.
	thirdLogged := func() bool {
		mu.Lock()
		defer mu.Unlock()
		launches := 0
		for _, l := range lines {
			switch {
			case l == "mini: connecting":
				launches++
			case l == "mini: connected" && launches >= 3:
				return true
			}
		}
		return false
	}
	waitFor(t, 10*time.Second, func() bool {
		return s.Launches() >= 3 && s.Status() == StatusConnected && thirdLogged()
	}, "the third master to connect and be logged")
	if n := connectedLines(); n != 2 {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("connected was reported %d times, want 2 (the first and third master, not the one that died):\n%s", n, strings.Join(lines, "\n"))
	}
	if s.Launches() != 3 {
		t.Fatalf("Launches = %d, want 3", s.Launches())
	}
	if !applied(t, s) {
		t.Fatal("the forward must be applied on the master that survived")
	}
	if n := strings.Count(readLog(t, logPath), "-O forward -L 127.0.0.1:8080:localhost:80"); n != 3 {
		t.Fatalf("forward requested %d times, want 3 (add, failed re-apply, re-apply)", n)
	}
}

func TestStopDuringReapplyIsNotReportedConnected(t *testing.T) {
	s, _ := newFakeSession(t)
	var mu sync.Mutex
	var lines []string
	s.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a Stop arrived before the pass finished
	exited := make(chan struct{})
	if s.markConnected(ctx, exited) {
		t.Fatal("markConnected reported success for a cancelled pass")
	}
	if s.Status() == StatusConnected {
		t.Fatal("status became connected although the session was being stopped")
	}
	close(exited)
	if s.markConnected(context.Background(), exited) || s.Status() == StatusConnected {
		t.Fatal("status became connected although the master had exited")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range lines {
		if l == "mini: connected" {
			t.Fatalf("connected was logged:\n%s", strings.Join(lines, "\n"))
		}
	}
}
