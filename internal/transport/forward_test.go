package transport

import (
	"context"
	"net"
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
	s, logPath := newFakeSession(t, "FAKE_SSH_FLAKY_ONCE=0.3")
	if err := s.AddForward(context.Background(), 8080, 80); err != nil {
		t.Fatalf("queueing a forward before connect should succeed: %v", err)
	}
	s.Start()
	waitFor(t, 5*time.Second, func() bool {
		return s.Launches() >= 2 && s.Status() == StatusConnected &&
			strings.Count(readLog(t, logPath), "-O forward -L 127.0.0.1:8080:localhost:80") >= 2
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
