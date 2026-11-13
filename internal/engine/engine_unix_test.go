//go:build unix

package engine

// The engine tests that only make sense on the ControlMaster transport,
// which Windows does not have. Each says why, and names the test that checks
// the same behaviour on every OS where there is one.

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// Unix only: it drops the connection by removing the ControlMaster's socket,
// a ControlMaster-only mechanism. Every OS: TestPerProcessRevivesAServerThatGaveUp
// checks the same revival on the per-process transport.
func TestStartRevivesAServerThatGaveUpUnderARunningProject(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	running := h.eng.Status("myapp")
	// The connection drops and the key is no longer accepted.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	sock := h.eng.servers["mini"].sess.(*transport.Session).SocketPath()
	h.eng.mu.Unlock()
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to give up")
	st := h.eng.Status("myapp")
	if st.State != StateDegraded || !strings.Contains(st.Message, "Permission denied") {
		t.Fatalf("status = %+v", st)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if got := h.eng.Status("myapp"); got.WebLocalPort != running.WebLocalPort || len(h.forwards("mini")) != 2 {
		t.Fatalf("status = %+v (was %+v), forwards %v", got, running, h.forwards("mini"))
	}
}

// Unix only: FAKE_SSH_FLAKY_ONCE makes a ControlMaster master die; the
// per-process link ignores it. Every OS:
// TestPerProcessRunningProjectBecomesDegradedWhenItsLinkDrops.
func TestRunningProjectBecomesDegradedWhenServerDrops(t *testing.T) {
	upPort := upstream(t, "ok")
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upPort))
	h := newHarness(t, cfg, "FAKE_SSH_FLAKY_ONCE=2")
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject web: %v", err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return h.eng.Status("myapp").State == StateDegraded }, "degraded state")
	st := h.eng.Status("myapp")
	if !strings.Contains(st.Message, "Broken pipe") || st.WebLocalPort == 0 {
		t.Fatalf("degraded status = %+v", st)
	}
	if web := h.eng.Status("web"); web.State != StateRunning {
		t.Fatalf("a local project is not affected by a server dropping: %+v", web)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if st := h.eng.Status("myapp"); st.Message != "" {
		t.Fatalf("recovered status = %+v", st)
	}

	// Both transitions are announced, and the supervisor's messages arrive as
	// log lines of the server.
	var states []State
	var sawSupervisor bool
	deadline := time.After(5 * time.Second)
	for len(states) < 4 || !sawSupervisor {
		select {
		case ev := <-events:
			// Degraded is announced once per reason; count the state once.
			if ev.Project == "myapp" && ev.State != "" && (len(states) == 0 || states[len(states)-1] != ev.State) {
				states = append(states, ev.State)
			}
			if ev.Server == "mini" && ev.Project == "" && strings.Contains(ev.Message, "reconnecting") {
				sawSupervisor = true
			}
		case <-deadline:
			t.Fatalf("events missing: states=%v supervisor=%v", states, sawSupervisor)
		}
	}
	want := []State{StateStarting, StateRunning, StateDegraded, StateRunning}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("state events = %v, want %v", states, want)
		}
	}
}

// Unix only: the master dies under an `-O forward` while it re-applies the
// forwards (FAKE_SSH_FORWARD_KILLS_MASTER); the per-process transport has no
// master and no re-apply over one. Every OS: a forward ended by its
// connection rather than by a taken port is degraded, not an error, with no
// holder lookup, in TestPerProcessLostForwardIsDegradedNotAnError.
func TestMasterDyingDuringReapplyIsNotALostForward(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-kills-master")
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_FORWARD_KILLS_MASTER="+flag)
	var lookups atomic.Int32
	h.eng.holderOf = func(context.Context, int) string { lookups.Add(1); return "" }
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	// The connection drops, and the next master dies while the forwards are
	// being re-applied on it. The one after that is fine.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		_, err := os.Stat(flag)
		return os.IsNotExist(err) && h.eng.Status("myapp").State == StateRunning
	}, "the project to be running on the third master")
	states, _ := stateEvents(events, "myapp")
	if want := []State{StateStarting, StateRunning, StateDegraded, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v: a master that died is not a port that could not be bound", states, want)
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("%d process lookups for forwards that only failed because the master died", n)
	}
	if got := h.unapplied("mini"); len(got) != 0 {
		t.Fatalf("unapplied forwards = %v", got)
	}
}

// Unix only: it holds the teardown inside a ControlMaster `-O cancel`
// (FAKE_SSH_CANCEL_WAIT). The per-process transport, the only one on Windows,
// cancels a forward by ending the ssh it owns; nothing there can be held, so
// the test cannot be built on it. What it checks is the engine's own
// bookkeeping around a teardown, which is the same code on either transport.
func TestStatusDuringTeardownKeepsTheAnnouncedState(t *testing.T) {
	for _, variant := range []string{"healthy", "after a lost forward"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			flag, wait := filepath.Join(dir, "forward-fails"), filepath.Join(dir, "cancel-waits")
			lb := testutil.FreePort(t)
			h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag, "FAKE_SSH_CANCEL_WAIT="+wait)
			t.Cleanup(func() { os.Remove(wait) }) // never leave a cancel waiting
			events, unsub := h.eng.Events().Subscribe(1024)
			defer unsub()
			var before ProjectStatus
			if variant == "healthy" {
				if err := h.eng.StartProject(bg, "myapp"); err != nil {
					t.Fatal(err)
				}
				before = h.eng.Status("myapp")
			} else {
				before = loseLoopbackForward(t, h, flag, lb)
			}
			stateEvents(events, "myapp") // drop what led here

			// The teardown stops inside its first forward cancel.
			if err := os.WriteFile(wait, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan error, 1)
			go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
			testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the teardown to reach a cancel")
			for i := 0; i < 20; i++ {
				during := h.eng.Status("myapp")
				if during.State != before.State || during.Message != before.Message {
					t.Fatalf("status during the teardown = %+v, want what was last announced: %+v", during, before)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if states, _ := stateEvents(events, "myapp"); len(states) != 0 {
				t.Fatalf("state events during the teardown: %v", states)
			}

			if err := os.Remove(wait); err != nil {
				t.Fatal(err)
			}
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if st := h.eng.Status("myapp"); st.State != StateStopped {
				t.Fatalf("status = %+v", st)
			}
			if states, _ := stateEvents(events, "myapp"); fmt.Sprint(states) != fmt.Sprint([]State{StateStopped}) {
				t.Fatalf("state events of the stop = %v, want [stopped]", states)
			}
		})
	}
}

// Unix only: it holds the teardown inside a ControlMaster `-O cancel`
// (FAKE_SSH_CANCEL_WAIT). The per-process transport, the only one on Windows,
// cancels a forward by ending the ssh it owns; nothing there can be held, so
// the test cannot be built on it. What it checks is the engine's own
// bookkeeping around a teardown, which is the same code on either transport.
func TestStartDuringAStopWaitsForItAndThenStarts(t *testing.T) {
	wait := filepath.Join(t.TempDir(), "cancel-waits")
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_CANCEL_WAIT="+wait)
	t.Cleanup(func() { os.Remove(wait) }) // never leave a cancel waiting
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	stateEvents(events, "myapp") // drop what led here

	// The stop is held inside its first forward cancel.
	if err := os.WriteFile(wait, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the teardown to reach a cancel")

	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	// It has nothing to report while the stop is under way. (A start that
	// only gets going after this pause is caught by what follows.)
	select {
	case err := <-started:
		t.Fatalf("StartProject returned %v while the project was being stopped", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.Remove(wait); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("StartProject after the stop: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the start never finished")
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.WebLocalPort == 0 {
		t.Fatalf("status = %+v: the start requested during the stop was lost", st)
	}
	if f := h.forwards("mini"); len(f) != 2 || len(h.unapplied("mini")) != 0 || len(h.eng.Sites()) != 1 {
		t.Fatalf("forwards = %v, unapplied %v, sites %v", f, h.unapplied("mini"), h.eng.Sites())
	}
	states, _ := stateEvents(events, "myapp")
	if want := []State{StateStopped, StateStarting, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v", states, want)
	}
}

// Unix only: it fails a ControlMaster `-O cancel` (FAKE_SSH_CANCEL_FAIL). On the
// per-process transport, the only one on Windows, CancelForward ends a
// process the transport owns and cannot fail, so a failed cancel does not
// happen there.
func TestFailedCancelAfterALostForwardEndsInErrorNamingTheCancel(t *testing.T) {
	dir := t.TempDir()
	flag, block := filepath.Join(dir, "forward-fails"), filepath.Join(dir, "cancel-fails")
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag, "FAKE_SSH_CANCEL_FAIL="+block)
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	loseLoopbackForward(t, h, flag, lb)
	stateEvents(events, "myapp")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err == nil {
		t.Fatal("expected the stop to report the failed cancel")
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, "stop myapp") || !strings.Contains(st.Message, "cancel") {
		t.Fatalf("status = %+v, want the failed stop", st)
	}
	states, last := stateEvents(events, "myapp")
	if fmt.Sprint(states) != fmt.Sprint([]State{StateError}) || last[StateError] != "state: error: "+st.Message {
		t.Fatalf("state events = %v (%q), want one error carrying %q", states, last[StateError], st.Message)
	}
	if f := h.forwards("mini"); len(f) != 1 {
		t.Fatalf("forwards = %v, want the web forward kept for a retry", f)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.forwards("mini")) != 0 {
		t.Fatalf("status = %+v, forwards %v", st, h.forwards("mini"))
	}
}

// Unix only: it fails a ControlMaster `-O cancel` (FAKE_SSH_CANCEL_FAIL). On the
// per-process transport, the only one on Windows, CancelForward ends a
// process the transport owns and cannot fail, so a failed cancel does not
// happen there.
func TestStopProjectReportsFailedCancelAndCanBeRetried(t *testing.T) {
	block := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_CANCEL_FAIL="+block)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	err := h.eng.StopProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("expected a cancel error, got %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "cancel") || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 0 {
		t.Fatal("the site must be gone even when a forward could not be cancelled")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.forwards("mini")) != 0 {
		t.Fatalf("status = %+v, forwards %v", st, h.forwards("mini"))
	}
}

// Unix only: it fails a ControlMaster `-O cancel` (FAKE_SSH_CANCEL_FAIL). On the
// per-process transport, the only one on Windows, CancelForward ends a
// process the transport owns and cannot fail, so a failed cancel does not
// happen there.
func TestShutdownReturnsTeardownErrorsAndStillStopsTheRest(t *testing.T) {
	block := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "ok")))
	h := newHarness(t, cfg, "FAKE_SSH_CANCEL_FAIL="+block)
	for _, name := range []string{"myapp", "web"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatal(err)
		}
	}
	err := h.eng.Shutdown(bg)
	if err == nil || !strings.Contains(err.Error(), "myapp") || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("expected Shutdown to report the failed cancel of myapp, got %v", err)
	}
	if !strings.Contains(h.log(), "-O exit -- mini") {
		t.Fatal("the master must still be stopped")
	}
	if st := h.eng.Status("web"); st.State != StateStopped {
		t.Fatalf("web = %+v", st)
	}
	h.assertNothingLeft(t)
}

// Unix only: the limit is the length of a ControlMaster's socket path. The
// per-process transport, the only one on Windows, makes no socket, so a long
// run directory is no error there.
func TestRunDirectoryTooLongForAControlSocketFailsTheStartAtOnce(t *testing.T) {
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) {
		o.RunDir = filepath.Join(o.RunDir, strings.Repeat("d", 80))
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
		t.Fatalf("StartProject = %v, want the socket path error", err)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("the failure took %s; it must not wait for a connection", took)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "too long") {
		t.Fatalf("status = %+v", st)
	}
	if log := h.log(); log != "" {
		t.Fatalf("ssh was run although its socket path cannot work:\n%s", log)
	}
}

// Unix only: FAKE_SSH_HANG stalls a ControlMaster master; the per-process
// link ignores it. Every OS: TestPerProcessShutdownInterruptsAStartInProgress
// holds the link with FAKE_SSH_LINK_HOLD.
func TestShutdownInterruptsAStartInProgress(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_HANG=1")
	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		return h.eng.Status("myapp").State == StateStarting && strings.Contains(h.log(), "-N -M")
	}, "the start to be waiting for ssh")
	begin := time.Now()
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("Shutdown waited %s for a start that could not finish", d)
	}
	if err := <-started; err == nil {
		t.Fatal("the interrupted start must report an error")
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.eng.Sites()) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

// Unix only: the test stands in for the forward by listening on 127.0.0.1
// itself, which the ControlMaster fake leaves free; the per-process fake's
// own forward listens there. Every OS:
// TestPerProcessLoopbackForwardAnswersOnTheIPv6Loopback.
//
// A loopback forward answers on [::1] too, through a relay to 127.0.0.1,
// until the project stops.
func TestLoopbackForwardAnswersOnTheIPv6Loopback(t *testing.T) {
	probe, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	lb := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	h := newHarness(t, remoteConfig(lb))
	mustStart(t, h, "myapp")
	// The harness's ssh is a fake, so this stands in for the forward.
	fwd, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", lb))
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	go func() {
		c, err := fwd.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	c, err := net.Dial("tcp", fmt.Sprintf("[::1]:%d", lb))
	if err != nil {
		t.Fatalf("dial [::1]: %v", err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	c.Close()
	if err != nil || string(got) != "ping" {
		t.Fatalf("read %q, %v", got, err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if c, err := net.Dial("tcp", fmt.Sprintf("[::1]:%d", lb)); err == nil {
		c.Close()
		t.Error("[::1] still answers after the project stopped")
	}
}
