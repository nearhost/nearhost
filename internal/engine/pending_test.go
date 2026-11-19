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

// downHarness is a started engine with the project "myapp" on the server
// "mini", which refuses every connection while the returned flag file
// exists, and a start that waits only briefly for it.
func downHarness(t *testing.T) (h *harness, down string) {
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
	return h, down
}

// waitingHarness is downHarness with "myapp" started and waiting for mini.
func waitingHarness(t *testing.T) (h *harness, down string) {
	t.Helper()
	h, down = downHarness(t)
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.HasPrefix(err.Error(), "waiting for mini") {
		t.Fatalf("StartProject = %v, want it to say it waits for mini", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateStarting || !strings.HasPrefix(st.Message, "waiting for mini: ") || !strings.Contains(st.Message, "Connection refused") {
		t.Fatalf("status = %+v, want starting, waiting for mini with the session's error", st)
	}
	if st.Waiting != "mini" {
		t.Fatalf("Waiting = %q, want mini", st.Waiting)
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

// stopMyapp stops "myapp" by hand.
func stopMyapp(h *harness) error { return h.eng.StopProject(bg, "myapp") }

// disableMyapp stops "myapp" as Apply does when the project is disabled.
func disableMyapp(h *harness) error {
	next := h.eng.Config()
	next.Projects[0].Enabled = false
	return h.eng.Apply(bg, next)
}

func TestStoppingAWaitingProjectDropsItsStart(t *testing.T) {
	h, down := waitingHarness(t)
	if err := stopMyapp(h); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
	h.connectStartsNothing(t, down)
}

func TestDisablingAWaitingProjectDropsItsStart(t *testing.T) {
	h, down := waitingHarness(t)
	if err := disableMyapp(h); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
	h.connectStartsNothing(t, down)
}

// A start made while a waiting project is being stopped runs after the
// stop, once. While the stop runs the project still shows what it waits for.
func TestAStartDuringTheStopOfAWaitingProjectRunsAfterIt(t *testing.T) {
	h, down := waitingHarness(t)
	held, release := make(chan struct{}), make(chan struct{})
	h.eng.stopHeld = func(string) {
		h.eng.stopHeld = nil // only this stop, not the harness's shutdown
		close(held)
		<-release
	}
	stopped := make(chan error, 1)
	go func() { stopped <- stopMyapp(h) }()
	<-held
	if st := h.eng.Status("myapp"); st.State != StateStarting || st.Waiting != "mini" {
		t.Fatalf("status during the stop = %+v, want it still starting and waiting for mini", st)
	}
	// The server is up for the start that follows.
	if err := os.Remove(down); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusConnected
	}, "mini to connect")
	queued := make(chan struct{})
	h.eng.startQueued = func(string) { close(queued) }
	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	select {
	case <-queued:
	case err := <-started:
		close(release)
		<-stopped
		t.Fatalf("StartProject during the stop returned %v without waiting for it", err)
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-started; err != nil {
		t.Fatalf("StartProject after the stop: %v", err)
	}
	h.eng.starts.Wait()
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Waiting != "" {
		t.Fatalf("status = %+v, want running", st)
	}
	if n := len(h.ensureCalls()); n != 1 {
		t.Fatalf("the project was started %d times, want once", n)
	}
	if n := len(h.forwards("mini")); n != 2 {
		t.Fatalf("mini carries %d forwards, want the web and loopback routes'", n)
	}
}

// connectStartsNothing lets mini connect and checks that "myapp", which a
// stop has dropped the waiting start of, does not start: it waits for
// nothing, a review of mini (what each message of its session runs) hands no
// start over, no start is under way, and the project serves nothing.
func (h *harness) connectStartsNothing(t *testing.T, down string) {
	t.Helper()
	if err := os.Remove(down); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusConnected
	}, "mini to connect")
	h.eng.mu.Lock()
	run := h.eng.runs["myapp"]
	waiting := run.waiting
	h.eng.reviewServerLocked("mini")
	resuming := run.resuming
	h.eng.mu.Unlock()
	h.eng.starts.Wait()
	if waiting || resuming {
		t.Fatalf("the stopped project still waits (waiting %v, resuming %v)", waiting, resuming)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || st.Waiting != "" {
		t.Fatalf("status = %+v, want the stopped project left stopped", st)
	}
	if len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 || len(h.ensureCalls()) != 0 {
		t.Fatal("a project stopped while it waited started when its server connected")
	}
}

// A stop that lands after the wait ran out, before the start records that
// it waits, leaves nothing waiting.
func TestAStopAsTheWaitRunsOutLeavesNothingWaiting(t *testing.T) {
	h, down := downHarness(t)
	stopped := make(chan error, 1)
	h.eng.beforeWaiting = func(ctx context.Context, name string) {
		go func() { stopped <- h.eng.StopProject(bg, name) }()
		<-ctx.Done() // the stop has cancelled the start, finding nothing waiting
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil {
		t.Fatal("StartProject = nil, want the stop's interruption")
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	h.connectStartsNothing(t, down)
}

// The same for a project disabled through Apply as its wait runs out.
func TestADisableAsTheWaitRunsOutLeavesNothingWaiting(t *testing.T) {
	h, down := downHarness(t)
	next := h.eng.Config()
	next.Projects[0].Enabled = false
	applied := make(chan error, 1)
	h.eng.beforeWaiting = func(ctx context.Context, _ string) {
		go func() { applied <- h.eng.Apply(bg, next) }()
		<-ctx.Done()
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil {
		t.Fatal("StartProject = nil, want the stop's interruption")
	}
	if err := <-applied; err != nil {
		t.Fatal(err)
	}
	h.connectStartsNothing(t, down)
}

// A stop that comes after the server connected, while the start handed over
// waits for the project's op, still drops it.
func TestStoppingAProjectWhoseStartWasHandedOverDropsIt(t *testing.T) {
	stopAfterTheHandOver(t, stopMyapp)
}

// The same for a project disabled through Apply.
func TestDisablingAProjectWhoseStartWasHandedOverDropsIt(t *testing.T) {
	stopAfterTheHandOver(t, disableMyapp)
}

// stopAfterTheHandOver has mini connect while "myapp" waits for it, holds
// the start handed over back, stops the project with stop and checks that
// the start was dropped.
func stopAfterTheHandOver(t *testing.T, stop func(*harness) error) {
	t.Helper()
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
	go func() { stopped <- stop(h) }()
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
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "Permission denied") || st.Waiting != "mini" {
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
