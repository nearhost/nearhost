package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// waitingHarness is an engine whose server "mini" refuses every connection
// while the returned flag file exists, with a start that waits only briefly
// for it. Project "myapp" has been started and waits for the server.
func waitingHarness(t *testing.T) (h *harness, down string) {
	t.Helper()
	wrapper, down := downWrapper(t)
	if err := os.WriteFile(down, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h = buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	h.eng.connectWait = 300 * time.Millisecond
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.HasPrefix(err.Error(), "waiting for mini") {
		t.Fatalf("StartProject = %v, want it to say it waits for mini", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateStarting || !strings.HasPrefix(st.Message, "waiting for mini: ") || !strings.Contains(st.Message, "Connection refused") {
		t.Fatalf("status = %+v, want starting, waiting for mini with the session's error", st)
	}
	if len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 {
		t.Fatal("a project waiting for its server serves nothing yet")
	}
	return h, down
}

func TestAProjectStartsOnceItsServerConnects(t *testing.T) {
	h, down := waitingHarness(t)
	// A second start while it waits starts afresh, and waits on in turn.
	if err := h.eng.StartProject(bg, "myapp"); err == nil || !strings.HasPrefix(err.Error(), "waiting for mini") {
		t.Fatalf("StartProject while waiting = %v", err)
	}
	if err := os.Remove(down); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		return h.eng.Status("myapp").State == StateRunning
	}, "myapp to start once mini connects, with no other StartProject")
	if st := h.eng.Status("myapp"); st.Message != "" || st.WebLocalPort == 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 1 {
		t.Fatalf("sites = %v", h.eng.Sites())
	}
	if n := len(h.forwards("mini")); n != 2 {
		t.Fatalf("mini carries %d forwards, want the web and loopback routes'", n)
	}
}

func TestStoppingAWaitingProjectDropsItsStart(t *testing.T) {
	h, down := waitingHarness(t)
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
	if err := os.Remove(down); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusConnected
	}, "mini to connect")
	time.Sleep(500 * time.Millisecond) // a start would be under way by now
	if st := h.eng.Status("myapp"); st.State != StateStopped {
		t.Fatalf("status = %+v, want the stopped project left stopped", st)
	}
	if len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 {
		t.Fatal("a project stopped while it waited started when its server connected")
	}
}

// A stop that comes after the server connected, while the start handed over
// waits for the project's op, still drops it.
func TestStoppingAProjectWhoseStartWasHandedOverDropsIt(t *testing.T) {
	h, down := waitingHarness(t)
	h.eng.mu.Lock()
	run := h.eng.runs["myapp"]
	h.eng.mu.Unlock()
	run.op.Lock() // holds the handed-over start back
	if err := os.Remove(down); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return run.resuming
	}, "mini to connect and myapp's start to be handed over")
	stopped := make(chan error, 1)
	go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
	dropped := false
	for deadline := time.Now().Add(2 * time.Second); !dropped && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		h.eng.mu.Lock()
		dropped = !run.resuming
		h.eng.mu.Unlock()
	}
	run.op.Unlock() // whatever happened: a held op would hold Shutdown too
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	h.eng.starts.Wait()
	if !dropped {
		t.Error("the stop did not drop the start handed over")
	}
	if n := len(h.ensureCalls()); n != 0 {
		t.Errorf("the start went on after the stop: it asked for the hosts %d times", n)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped {
		t.Fatalf("status = %+v, want stopped", st)
	}
	if len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 {
		t.Fatal("a project stopped after its server connected started all the same")
	}
}

func TestShutdownWhileAProjectWaitsReturns(t *testing.T) {
	h, _ := waitingHarness(t)
	ctx, cancel := context.WithTimeout(bg, 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.eng.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not return while a project waited for its server")
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
}

// A start that met a server which gave up (authentication failed) shows that
// error as it always has, and still starts when the server connects later.
func TestAProjectWhoseServerGaveUpStartsWhenItConnects(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("StartProject = %v, want the authentication error", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "Permission denied") {
		t.Fatalf("status = %+v", st)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ReconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		return h.eng.Status("myapp").State == StateRunning
	}, "myapp to start once mini connects")
}
