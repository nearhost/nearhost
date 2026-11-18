package transport

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

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

// logLines returns the argv lines the fake ssh recorded.
func logLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(readLog(t, path)), "\n")
}

// slowClock stands in for the clock and the timer of a supervisor's wait
// after it gave up. Its time starts at 01:49 UTC, which is 14:49 in its zone,
// and moves on only when a wait is let go.
type slowClock struct {
	mu      sync.Mutex
	now     time.Time
	zone    *time.Location
	waits   []time.Duration // every wait asked for, in order
	stopped int             // waits stopped before they ended
	pending chan time.Time  // the wait under way, or nil
}

// newSlowClock puts a slowClock in v.
func newSlowClock(v *supervision) *slowClock {
	c := &slowClock{
		now:  time.Date(2026, 11, 18, 1, 49, 0, 0, time.UTC),
		zone: time.FixedZone("NZDT", 13*60*60),
	}
	v.now = c.Now
	v.zone = c.zone
	v.newTimer = c.newTimer
	return c
}

// Now is the clock's time.
func (c *slowClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *slowClock) newTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waits = append(c.waits, d)
	c.pending = ch
	return ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.pending != ch {
			return false
		}
		c.pending = nil
		c.stopped++
		return true
	}
}

// Waits returns the waits asked for so far.
func (c *slowClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// Stopped returns how many waits were stopped before they ended.
func (c *slowClock) Stopped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// Pass lets the wait under way end, the clock moving on by its length.
func (c *slowClock) Pass(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		t.Fatal("no wait is under way")
	}
	c.now = c.now.Add(c.waits[len(c.waits)-1])
	c.pending <- c.now
	c.pending = nil
}

// slowRetry is a transport that refuses logins while the file refuse
// exists, with a slowClock, and launches, which counts its connection
// attempts.
type slowRetry struct {
	tr       Transport
	clock    *slowClock
	refuse   string
	launches func() int
}

// gaveUp waits for the transport to give up for now after its attempt
// number n, and checks it waits authRetryEvery.
func (r slowRetry) gaveUp(t *testing.T, n, waits int) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		return r.tr.Status() == StatusAuthFailed && len(r.clock.Waits()) == waits
	}, fmt.Sprintf("wait %d after giving up", waits))
	if got := r.launches(); got != n {
		t.Fatalf("attempts = %d, want %d", got, n)
	}
	for i, d := range r.clock.Waits() {
		if d != authRetryEvery {
			t.Fatalf("wait %d = %s, want %s", i+1, d, authRetryEvery)
		}
	}
}

// retriesEvery15Minutes checks that after three refused logins the
// transport tries once per authRetryEvery, staying auth failed and saying
// when it tries next, until a login succeeds.
func (r slowRetry) retriesEvery15Minutes(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	h := r.tr.Health()
	if !strings.Contains(h.LastError, "Permission denied") || !strings.HasSuffix(h.LastError, "; trying again at 15:04") {
		t.Fatalf("LastError = %q", h.LastError)
	}
	if want := r.clock.Now().Add(authRetryEvery); !h.NextAttempt.Equal(want) || h.Failures != 3 {
		t.Fatalf("health %+v, want the next attempt at %s", h, want)
	}
	time.Sleep(100 * time.Millisecond) // no attempt until the wait ends
	if n := r.launches(); n != 3 || r.tr.Status() != StatusAuthFailed {
		t.Fatalf("attempts = %d, status %s while waiting", n, r.tr.Status())
	}

	r.clock.Pass(t)
	r.gaveUp(t, 4, 2) // one refused attempt, then the long wait again
	if h := r.tr.Health(); !strings.HasSuffix(h.LastError, "; trying again at 15:19") || h.Failures != 4 {
		t.Fatalf("health %+v", h)
	}

	if err := os.Remove(r.refuse); err != nil {
		t.Fatal(err)
	}
	r.clock.Pass(t)
	waitFor(t, 5*time.Second, func() bool { return r.tr.Status() == StatusConnected }, "connected")
	if n, h := r.launches(), r.tr.Health(); n != 5 || h.Failures != 0 || !h.NextAttempt.IsZero() {
		t.Fatalf("attempts = %d, health %+v once connected", n, h)
	}
}

// startRetriesAtOnce checks that Start, during the long wait, ends it and
// tries again at once with the usual backoff, and that three more refusals
// lead back to the long wait.
func (r slowRetry) startRetriesAtOnce(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	r.tr.Start()
	r.gaveUp(t, 6, 2)
	if s := r.clock.Stopped(); s != 1 {
		t.Fatalf("waits stopped = %d, want 1", s)
	}
	if h := r.tr.Health(); h.Failures != 3 {
		t.Fatalf("Failures = %d, want 3 counted afresh", h.Failures)
	}
}

// stopEndsTheWait checks that Stop ends the long wait at once.
func (r slowRetry) stopEndsTheWait(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	r.tr.Stop()
	if st := r.tr.Status(); st != StatusDisconnected {
		t.Fatalf("status after Stop = %s", st)
	}
	if s := r.clock.Stopped(); s != 1 {
		t.Fatalf("waits stopped = %d, want 1", s)
	}
	time.Sleep(100 * time.Millisecond)
	if n := r.launches(); n != 3 {
		t.Fatalf("attempts = %d after Stop, want 3", n)
	}
}

// beforeRetryTime is msg, the error of a transport that gave up for now,
// without the time of its next attempt, which it checks is there.
func beforeRetryTime(t *testing.T, msg string) string {
	t.Helper()
	before, at, ok := strings.Cut(msg, "; trying again at ")
	if !ok || len(at) != len("15:04") {
		t.Fatalf("LastError = %q, want it to end with the time of the next attempt", msg)
	}
	return before
}
