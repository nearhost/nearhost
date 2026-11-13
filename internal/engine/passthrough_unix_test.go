//go:build unix

package engine

// The passthrough tests that only make sense on the ControlMaster
// transport, which Windows does not have. Each says why, and names the test
// that checks the same behaviour on every OS where there is one.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// Unix only: it fails a ControlMaster `-O cancel` (FAKE_SSH_CANCEL_FAIL). On the
// per-process transport, the only one on Windows, CancelForward ends a
// process the transport owns and cannot fail, so a failed cancel does not
// happen there.
func TestFailedCancelOfClosedOpenKeepsPassthroughUntilChecked(t *testing.T) {
	failFile := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(failFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h, _ := endHarness(t, "", "FAKE_SSH_CANCEL_FAIL="+failFile)
	h.eng.beforeForward = func(id string) {
		if err := h.eng.ClosePassthrough(bg, id, "done"); err != nil {
			t.Error(err)
		}
	}
	port := testutil.FreePort(t)
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("open err = %v, want the failed cancel", err)
	}
	if r := record(h, "p1"); r == nil || !r.closing || r.opening {
		t.Fatalf("record = %+v, want p1 kept closing so it can be closed", r)
	}
	if got := h.eng.Passthroughs(""); len(got) != 0 {
		t.Fatalf("passthroughs = %v, want the closing p1 left out", got)
	}
	if h.eng.PortFree(port) {
		t.Fatal("the port was freed while its forward is still live")
	}
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	h.eng.checkPassthroughs(bg)
	if got := h.eng.Passthroughs(""); len(got) != 0 {
		t.Fatalf("passthroughs = %v after the check", got)
	}
	if !h.eng.PortFree(port) {
		t.Fatal("the port is still held after the check closed it")
	}
	if got := h.forwards("apollo"); len(got) != 0 {
		t.Fatalf("forwards = %v", got)
	}
}

// Unix only: it fails a ControlMaster `-O cancel` (FAKE_SSH_CANCEL_FAIL). On the
// per-process transport, the only one on Windows, CancelForward ends a
// process the transport owns and cannot fail, so a failed cancel does not
// happen there.
func TestFailedCancelOfClosedOpenDuringShutdownReleases(t *testing.T) {
	dir := t.TempDir()
	failFile, waitFile := filepath.Join(dir, "cancel-fails"), filepath.Join(dir, "cancel-waits")
	if err := os.WriteFile(failFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h, _ := endHarness(t, "", "FAKE_SSH_CANCEL_FAIL="+failFile, "FAKE_SSH_CANCEL_WAIT="+waitFile)
	t.Cleanup(func() { os.Remove(waitFile) }) // never leave a cancel waiting
	h.eng.beforeForward = func(id string) {
		if err := h.eng.ClosePassthrough(bg, id, "done"); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(waitFile, nil, 0o600); err != nil {
			t.Error(err)
		}
	}
	port := testutil.FreePort(t)
	done := make(chan error, 1)
	go func() {
		_, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
		done <- err
	}()
	// The undo waits inside its cancel while the engine shuts down, so the
	// cancel fails after Shutdown has begun. Shutdown waits for the open.
	testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the undo to reach its cancel")
	shut := make(chan error, 1)
	go func() { shut <- h.eng.Shutdown(bg) }()
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return h.eng.closed
	}, "Shutdown to begin")
	if err := os.Remove(waitFile); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("the open succeeded")
	}
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if got := h.eng.Passthroughs(""); len(got) != 0 {
		t.Fatalf("passthroughs = %v after Shutdown", got)
	}
	h.eng.mu.Lock()
	_, held := h.eng.ports[port]
	_, relay := h.eng.relays[port]
	h.eng.mu.Unlock()
	if held || relay {
		t.Fatalf("after Shutdown the port is held %v, relayed %v", held, relay)
	}
}

// Unix only: it holds a close inside the ControlMaster fake's own
// `-O cancel` (FAKE_SSH_CANCEL_WAIT), which the per-process ssh does not
// have. Every OS: TestPerProcessAuthFailedClosesPassthroughs checks both
// halves, the passthroughs closing and the server's log not blocked while a
// close is cancelling, holding the cancel through a wrapper of the
// transport (harness.holdCancels).
func TestAuthFailedClosesPassthroughs(t *testing.T) {
	ssh, flag, _ := authWrapper(t)
	wait := filepath.Join(t.TempDir(), "cancel-waits")
	h, _ := endHarness(t, ssh, "FAKE_SSH_CANCEL_WAIT="+wait)
	events, cancel := h.eng.bus.Subscribe(256)
	defer cancel()
	p := openP1(t, h)
	other := passthroughSpec(testutil.FreePort(t))
	other.ID = "p2"
	if _, err := h.eng.OpenPassthrough(bg, other); err != nil {
		t.Fatal(err)
	}
	// Closing p2 holds the session's forward lock until wait goes.
	if err := os.WriteFile(wait, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	closing := make(chan error, 1)
	go func() { closing <- h.eng.ClosePassthrough(bg, "p2", "the agent finished") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "p2's cancel to start")
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "apollo")
	waitStatus(t, h, "auth failed", func(st transport.Status) bool { return st == transport.StatusAuthFailed })
	logged := make(chan struct{})
	go func() {
		h.eng.serverLog("apollo", "still failing")
		close(logged)
	}()
	select {
	case <-logged:
	case <-time.After(5 * time.Second):
		t.Fatal("serverLog blocked while the forward lock was held")
	}
	if err := os.Remove(wait); err != nil {
		t.Fatal(err)
	}
	if err := <-closing; err != nil {
		t.Fatal(err)
	}
	waitClosedEvent(t, events, p.Remote, "apollo's connection failed: user@host: Permission denied (publickey).")
	assertClosed(t, h, p.Local)
}

// Unix only: it holds the cancel inside the ControlMaster fake's own
// `-O cancel` (FAKE_SSH_CANCEL_WAIT), which the per-process ssh does not
// have. Every OS: TestPerProcessCloseKeepsRecordUntilCancelled holds it
// through a wrapper of the transport (harness.holdCancels).
func TestCloseKeepsRecordUntilCancelled(t *testing.T) {
	port := testutil.FreePort(t)
	wait, _ := cancelFiles(t)
	h, _ := endHarness(t, "", "FAKE_SSH_CANCEL_WAIT="+wait)
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err != nil {
		t.Fatal(err)
	}
	touch(t, wait)
	done := make(chan error, 1)
	go func() { done <- h.eng.ClosePassthrough(bg, "p1", "done") }()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the close to reach its cancel")

	if got := h.eng.Passthroughs(""); len(got) != 0 {
		t.Fatalf("Passthroughs = %+v while it closes", got)
	}
	// A project's start claims its loopback ports this way. It cannot be
	// started here: its web forward would wait for the session, which the
	// cancel holds.
	if holder, ok := h.eng.claimPortFor("one", port); ok || holder != "a passthrough for claude-code on apollo (session 1a2b3c4d)" {
		t.Fatalf("a project's claim got ok %v, holder %q", ok, holder)
	}
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err == nil {
		t.Fatal("an open with the id of a closing passthrough was accepted")
	}
	if h.eng.PortFree(port) {
		t.Fatal("the port was freed before the cancel ended")
	}

	remove(t, wait)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if record(h, "p1") != nil {
		t.Fatal("the record is kept after the cancel")
	}
	if !h.eng.PortFree(port) {
		t.Fatal("the port is still held after the cancel")
	}
}

// Unix only: it holds the cancel inside the ControlMaster fake's own
// `-O cancel` (FAKE_SSH_CANCEL_WAIT), which the per-process ssh does not
// have. Every OS: TestPerProcessConcurrentClosesReleaseOnce holds it
// through a wrapper of the transport (harness.holdCancels).
// The failing variant needs a failed cancel, which per-process cannot have.
func TestConcurrentClosesReleaseOnce(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(fmt.Sprintf("failing=%v", failing), func(t *testing.T) {
			wait, fail := cancelFiles(t)
			h, _ := endHarness(t, "", "FAKE_SSH_CANCEL_WAIT="+wait, "FAKE_SSH_CANCEL_FAIL="+fail)
			events, cancel := h.eng.bus.Subscribe(64)
			defer cancel()
			p := openP1(t, h)
			touch(t, wait)
			if failing {
				touch(t, fail)
			}
			first := make(chan error, 1)
			go func() { first <- h.eng.ClosePassthrough(bg, "p1", "done") }()
			testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the first close to reach its cancel")
			// The second close returns while the first still waits.
			if err := h.eng.ClosePassthrough(bg, "p1", "done again"); err != nil {
				t.Fatalf("second close: %v", err)
			}
			remove(t, wait)
			err := <-first
			if failing {
				if err == nil {
					t.Fatal("the first close succeeded with a failing cancel")
				}
				if h.eng.PortFree(p.Local) {
					t.Fatal("the port was freed by a failed close")
				}
				remove(t, fail)
				h.eng.checkPassthroughs(bg)
			} else if err != nil {
				t.Fatal(err)
			}
			assertClosed(t, h, p.Local)
			if n := strings.Count(h.log(), "-O cancel"); n != 1+btoi(failing) {
				t.Fatalf("%d cancels, want %d:\n%s", n, 1+btoi(failing), h.log())
			}
			if n := closedEvents(events, p.Remote); n != 1 {
				t.Fatalf("%d close events, want 1", n)
			}
		})
	}
}

// Unix only: it fails a ControlMaster `-O cancel` (FAKE_SSH_CANCEL_FAIL). On the
// per-process transport, the only one on Windows, CancelForward ends a
// process the transport owns and cannot fail, so a failed cancel does not
// happen there.
func TestFailedCloseStaysClosing(t *testing.T) {
	_, fail := cancelFiles(t)
	h, _ := endHarness(t, "", "FAKE_SSH_CANCEL_FAIL="+fail)
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	p := openP1(t, h)
	touch(t, fail)
	if err := h.eng.ClosePassthrough(bg, "p1", "the agent finished"); err == nil {
		t.Fatal("the close succeeded with a failing cancel")
	}
	if r := record(h, "p1"); r == nil || !r.closing {
		t.Fatalf("record = %+v, want it kept with closing set", r)
	}
	if got := h.eng.Passthroughs(""); len(got) != 0 {
		t.Fatalf("Passthroughs = %+v, want the closing one left out", got)
	}
	if h.eng.PortFree(p.Local) {
		t.Fatal("the port was freed by a failed close")
	}
	remove(t, fail)
	h.eng.checkPassthroughs(bg)
	if record(h, "p1") != nil {
		t.Fatal("the check did not close it")
	}
	assertClosed(t, h, p.Local)
	waitClosedEvent(t, events, p.Remote, "the agent finished")
}

// cancelFiles returns the paths of the files that, while they exist, make
// the fake ssh's cancels wait and fail. Neither exists yet.
func cancelFiles(t *testing.T) (wait, fail string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "cancel-waits"), filepath.Join(dir, "cancel-fails")
}

// touch creates the empty file path, and removes it when the test ends,
// before the cleanups registered earlier run: the harness's Shutdown must
// not find a cancel waiting on it.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
}

// remove deletes the file path.
func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// closedEvents counts the close events of the passthrough on remote that are
// waiting in events.
func closedEvents(events <-chan Event, remote int) int {
	n := 0
	prefix := fmt.Sprintf("closed port %d for ", remote)
	for len(events) > 0 {
		if ev := <-events; strings.HasPrefix(ev.Message, prefix) {
			n++
		}
	}
	return n
}

// record returns the engine's record of the passthrough id, or nil.
func record(h *harness, id string) *passthrough {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	return h.eng.passthroughs[id]
}

// btoi is 1 for true and 0 for false.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
