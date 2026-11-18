package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
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

// newSlowClock gives v a testutil.FakeClock, and returns it.
func newSlowClock(v *supervision) *testutil.FakeClock {
	c := testutil.NewFakeClock()
	v.clock = Clock{Now: c.Now, Zone: c.Zone(), NewTimer: c.NewTimer, NewTicker: c.NewTicker}
	return c
}

// slowRetry is a transport that refuses logins while the file refuse
// exists, with a slowClock, and launches, which counts its connection
// attempts. Made held, it refuses every login, and each attempt waits
// while the file hold exists.
type slowRetry struct {
	tr       Transport
	v        *supervision
	clock    *testutil.FakeClock
	refuse   string
	hold     string
	launches func() int
}

// How a slowRetry's ssh fails: it refuses while the file refuse exists;
// held, it refuses always and each attempt waits while the file hold
// exists; alternating, it refuses every other attempt, starting with the
// first, and fails another way in between.
const (
	refusing = iota
	held
	alternating
)

// slowRetryEnv makes the files of a slowRetry and returns them with the
// fake ssh's environment for them, failing as mode says.
func slowRetryEnv(t *testing.T, mode int) (refuse, hold string, env []string) {
	t.Helper()
	dir := t.TempDir()
	refuse, hold = filepath.Join(dir, "refuse"), filepath.Join(dir, "hold")
	switch mode {
	case alternating:
		return refuse, hold, []string{"FAKE_SSH_ALTERNATE=1", "FAKE_SSH_ALTERNATE_FILE=" + filepath.Join(dir, "turn")}
	case held:
		env = []string{"FAKE_SSH_AUTH_FAIL_HOST=mini", "FAKE_SSH_AUTH_HOLD=" + hold}
	}
	if err := os.WriteFile(refuse, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return refuse, hold, append(env, "FAKE_SSH_AUTH_FAIL_FILE="+refuse)
}

// refusalTimes records, on the fake clock, when each refused login
// failed. Call it before Start.
func (r slowRetry) refusalTimes() func() []time.Time {
	var mu sync.Mutex
	var times []time.Time
	r.v.Logf = func(format string, args ...any) {
		if len(args) < 3 {
			return
		}
		st, _ := args[1].(Status)
		msg, _ := args[2].(string)
		if (st == StatusReconnecting || st == StatusAuthFailed) && strings.Contains(msg, "Permission denied") {
			mu.Lock()
			times = append(times, r.clock.Now())
			mu.Unlock()
		}
	}
	return func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), times...)
	}
}

// gaveUp waits for the transport to give up for now after its attempt
// number n, and checks it waits authRetryEvery each time.
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
// when it tries next, until a login succeeds; and that once it has
// connected, a refusal is tried three times again before the wait.
func (r slowRetry) retriesEvery15Minutes(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	h := r.tr.Health()
	if want := "user@host: Permission denied (publickey). Nearhost tries again at 15:04."; h.LastError != want {
		t.Fatalf("LastError = %q, want %q", h.LastError, want)
	}
	if want := r.clock.Now().Add(authRetryEvery); !h.NextAttempt.Equal(want) || h.Failures != 3 || !h.Refused {
		t.Fatalf("health %+v, want the next attempt at %s", h, want)
	}
	time.Sleep(100 * time.Millisecond) // no attempt until the wait ends
	if n := r.launches(); n != 3 || r.tr.Status() != StatusAuthFailed {
		t.Fatalf("attempts = %d, status %s while waiting", n, r.tr.Status())
	}

	r.clock.Pass(t)
	r.gaveUp(t, 4, 2) // one refused attempt, then the long wait again
	if h := r.tr.Health(); !strings.HasSuffix(h.LastError, " Nearhost tries again at 15:19.") || h.Failures != 4 {
		t.Fatalf("health %+v", h)
	}

	if err := os.Remove(r.refuse); err != nil {
		t.Fatal(err)
	}
	r.clock.Pass(t)
	waitFor(t, 5*time.Second, func() bool { return r.tr.Status() == StatusConnected }, "connected")
	if n, h := r.launches(), r.tr.Health(); n != 5 || h.Failures != 0 || !h.NextAttempt.IsZero() || h.Refused {
		t.Fatalf("attempts = %d, health %+v once connected", n, h)
	}

	// Connected since, the transport tries three times again.
	if err := os.WriteFile(r.refuse, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.tr.Stop()
	r.tr.Start()
	r.gaveUp(t, 8, 3)
}

// revivesTryOnceEach checks that a Start during the long wait, or a Stop
// and a Start, makes one attempt at once, and a refusal leads straight
// back to the wait.
func (r slowRetry) revivesTryOnceEach(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	for i := 1; i <= 5; i++ {
		r.tr.Start()
		r.gaveUp(t, 3+i, 1+i)
		if h := r.tr.Health(); h.Failures != 1 {
			t.Fatalf("Failures = %d after revive %d, want 1 counted afresh", h.Failures, i)
		}
	}
	r.tr.Stop()
	r.tr.Start()
	r.gaveUp(t, 9, 7)
	if s := r.clock.Stopped(); s != 6 {
		t.Fatalf("waits stopped = %d, want 6", s)
	}
}

// revivesShareAnAttempt checks that Starts while the one attempt a revive
// made is under way make no attempt of their own. r must be held.
func (r slowRetry) revivesShareAnAttempt(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	if err := os.WriteFile(r.hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.tr.Start()
	// Connecting at once, so nothing takes the session for one that gave
	// up and replaces it before its loop has begun the attempt.
	if h := r.tr.Health(); h.Status != StatusConnecting || !h.NextAttempt.IsZero() {
		t.Fatalf("health %+v right after the revive, want connecting", h)
	}
	waitFor(t, 5*time.Second, func() bool { return r.launches() == 4 }, "the revived attempt")
	for range 4 {
		r.tr.Start()
	}
	if err := os.Remove(r.hold); err != nil {
		t.Fatal(err)
	}
	r.gaveUp(t, 4, 2)
	time.Sleep(100 * time.Millisecond)
	if n := r.launches(); n != 4 {
		t.Fatalf("attempts = %d, want the one the revives shared", n)
	}
}

// refusalsCountAcrossOtherFailures checks, for a transport whose ssh
// alternates, that refusals count since the last connect whatever fails
// between them: the third gives up for now. In the wait, another failure
// is retried in backoff and the next refusal goes straight back to the
// wait. From the start, no 10 minutes see more than three refused logins,
// and after those, one per 15 minutes.
func (r slowRetry) refusalsCountAcrossOtherFailures(t *testing.T) {
	refusals := r.refusalTimes()
	r.tr.Start()
	r.gaveUp(t, 5, 1) // refused, other, refused, other, refused
	for i := 2; i <= 4; i++ {
		r.clock.Pass(t)
		r.gaveUp(t, 2*i+3, i) // other, then refused
	}
	got := refusals()
	if len(got) != 6 {
		t.Fatalf("%d refused logins, want 6: %v", len(got), got)
	}
	for i, at := range got {
		n := 0
		for _, other := range got {
			if !other.Before(at) && other.Sub(at) < 10*time.Minute {
				n++
			}
		}
		if n > 3 {
			t.Fatalf("%d refused logins in the 10 minutes from %s, want at most 3: %v", n, at, got)
		}
		if i >= 3 && at.Sub(got[i-1]) < authRetryEvery {
			t.Fatalf("refused login %d came %s after the one before, want %s: %v", i+1, at.Sub(got[i-1]), authRetryEvery, got)
		}
	}
}

// triesWhenTheWallClockPasses checks that a wait whose timer has not fired
// ends at the next tick once the wall clock has passed the time it
// announced, as after the computer slept.
func (r slowRetry) triesWhenTheWallClockPasses(t *testing.T) {
	r.tr.Start()
	r.gaveUp(t, 3, 1)
	waitFor(t, 5*time.Second, func() bool { return r.clock.Tickers() == 1 }, "the wall clock's ticker")
	r.clock.Tick(t) // not yet time
	time.Sleep(100 * time.Millisecond)
	if n := r.launches(); n != 3 {
		t.Fatalf("attempts = %d before the announced time, want 3", n)
	}
	r.clock.Jump(16 * time.Minute)
	r.clock.Tick(t)
	r.gaveUp(t, 4, 2)
	if h := r.tr.Health(); !strings.HasSuffix(h.LastError, " Nearhost tries again at 15:20.") {
		t.Fatalf("LastError = %q", h.LastError)
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
	if s, n := r.clock.Stopped(), r.clock.Tickers(); s != 1 || n != 0 {
		t.Fatalf("timers stopped = %d, tickers running = %d, want 1 and 0", s, n)
	}
	time.Sleep(100 * time.Millisecond)
	if n := r.launches(); n != 3 {
		t.Fatalf("attempts = %d after Stop, want 3", n)
	}
}

// beforeRetryTime is msg, the error of a transport that gave up for now,
// without the sentence that says when it tries next, which it checks is
// there.
func beforeRetryTime(t *testing.T, msg string) string {
	t.Helper()
	before, at, ok := strings.Cut(msg, " Nearhost tries again at ")
	if !ok || len(at) != len("15:04.") || !strings.HasSuffix(at, ".") {
		t.Fatalf("LastError = %q, want it to end with the time of the next attempt", msg)
	}
	return before
}

// resetSSHPath forgets the ssh found on PATH, now and when t ends, so a fake
// ssh one test puts on PATH is not the one the next test runs.
func resetSSHPath(t *testing.T) {
	t.Helper()
	forget := func() {
		foundSSH.mu.Lock()
		foundSSH.path = ""
		foundSSH.mu.Unlock()
	}
	forget()
	t.Cleanup(forget)
}
