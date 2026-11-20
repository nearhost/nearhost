package transport

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// noticeBound is how soon after ssh exits a transport must say so, though a
// child of ssh holds its stderr: well under procWaitDelay, which Wait once
// waited on such a child for.
const noticeBound = time.Second

// stderrHolder returns the environment that has every fake master and plain
// ssh start a child holding its stderr, as a ProxyCommand or ProxyJump child
// does, until the test ends.
func stderrHolder(t *testing.T) string {
	t.Helper()
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Registered before the transport's own cleanup, so it runs after the
	// transport has stopped: the children hold on until then.
	t.Cleanup(func() { os.Remove(hold) })
	return "FAKE_SSH_STDERR_HOLDER=" + hold
}

// stderrReaders counts the goroutines copying an ssh's stderr.
func stderrReaders() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "transport.copyStderr(")
}

// waitNoStderrReaders waits for every stderr reader to end, while the
// children holding the pipes still live.
func waitNoStderrReaders(t *testing.T) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool { return stderrReaders() == 0 }, "the stderr readers to end")
}

// counter counts the calls of a Notify.
func counter() (*atomic.Int32, func()) {
	var n atomic.Int32
	return &n, func() { n.Add(1) }
}

// waitNotified waits for a Notify call past the n0 counted before the loss.
// A transport records a loss and then calls Notify, so a test that has just
// read the loss may be ahead of the call.
func waitNotified(t *testing.T, notified *atomic.Int32, n0 int32, what string) {
	t.Helper()
	deadline := time.Now().Add(noticeBound)
	for notified.Load() == n0 {
		if time.Now().After(deadline) {
			t.Errorf("Notify was not told of %s", what)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPerProcessLinkDropIsNoticedWhileAChildHoldsStderr(t *testing.T) {
	p := newTestPerProcess(t, stderrHolder(t))
	notified, notify := counter()
	p.Notify = notify
	p.Start()
	waitPerProcessConnected(t, p)
	n0 := notified.Load()
	start := time.Now()
	p.drop(t, p.linkPid())
	waitFor(t, noticeBound, func() bool { return p.Status() != StatusConnected }, "the drop to be noticed")
	waitNotified(t, notified, n0, "the drop")
	t.Logf("noticed after %s", time.Since(start).Round(time.Millisecond))
	// Why it dropped is read after, from what ssh wrote last.
	waitFor(t, 5*time.Second, func() bool { return strings.Contains(p.LastError(), "Broken pipe") }, "the drop's message")
	p.Stop()
	waitNoStderrReaders(t)
}

func TestPerProcessForwardDropIsNoticedWhileAChildHoldsStderr(t *testing.T) {
	p := newTestPerProcess(t, stderrHolder(t))
	notified, notify := counter()
	p.Notify = notify
	p.Start()
	waitPerProcessConnected(t, p)
	local := freePort(t)
	if err := p.AddForward(t.Context(), local, 80); err != nil {
		t.Fatal(err)
	}
	applied := func() bool {
		_, states := p.ForwardStates()
		return len(states) == 1 && states[0].Applied
	}
	if !applied() {
		t.Fatal("the forward is not applied")
	}
	n0 := notified.Load()
	p.drop(t, p.forwardPid(local, 80))
	waitFor(t, noticeBound, func() bool { return !applied() }, "the lost forward to be noticed")
	waitNotified(t, notified, n0, "the lost forward")
	waitFor(t, 5*time.Second, func() bool {
		_, states := p.ForwardStates()
		return len(states) == 1 && (states[0].Applied || strings.Contains(states[0].Err, "Broken pipe"))
	}, "the lost forward's message")
	p.Stop()
	waitNoStderrReaders(t)
}

// A refused login is told from the line ssh writes as it exits, which is
// still read while a child holds the pipe.
func TestPerProcessRefusedLoginIsClassifiedWhileAChildHoldsStderr(t *testing.T) {
	p := newTestPerProcess(t, "FAKE_SSH_AUTH_FAIL=1", stderrHolder(t))
	p.Start()
	waitFor(t, 10*time.Second, func() bool { return p.Status() == StatusAuthFailed }, "the refused logins to count")
	if !strings.Contains(p.LastError(), "Permission denied") {
		t.Errorf("LastError = %q", p.LastError())
	}
	p.Stop()
	waitNoStderrReaders(t)
}
