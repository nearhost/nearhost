package transport

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// startSSH starts cmd, an ssh, and on Windows puts it in the job that ends
// it when nearhost ends. A failure to do that is logged: ssh still runs,
// though it may outlive a nearhost that is killed.
func (v *supervision) startSSH(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := joinJob(cmd); err != nil {
		v.Logf("%s: ssh (pid %d) may outlive Nearhost: %v", v.Name, cmd.Process.Pid, err)
	}
	return nil
}

// runSSH is cmd.Run for an ssh, started with startSSH.
func (v *supervision) runSSH(cmd *exec.Cmd) error {
	if err := v.startSSH(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}

// configConflict runs cmd, an `ssh -G` for host, and returns a *ConfigError
// when its output sets one of the options in only (see configConflicts). It
// returns nil when ssh -G itself fails: an old ssh or a passing error is no
// reason to refuse a connection that the real ssh may still make.
func (v *supervision) configConflict(cmd *exec.Cmd, host string, only ...string) error {
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := v.runSSH(cmd); err != nil {
		v.Logf("%s: ssh -G: %v", v.Name, err)
		return nil
	}
	return configConflicts(host, out.String(), only...)
}

// authRetryEvery is how long a supervisor waits after each attempt once
// maxAuthFailures attempts since the last connect have failed in a way
// that retrying seldom fixes, such as a refused login. A server whose sshd answers before
// its home directories are ready, or a laptop whose ssh agent wakes late,
// recovers by itself. ssh runs in BatchMode, so each attempt is one failed
// login on the server, and a strict fail2ban (five failures in ten
// minutes) must not ban the laptop for them.
const authRetryEvery = 15 * time.Minute

// wallCheckEvery is how often a supervisor waiting authRetryEvery also
// reads the wall clock. Its timer does not run while the computer sleeps,
// so after a sleep the time it announced may have passed long before the
// timer fires.
const wallCheckEvery = 30 * time.Second

// Clock is the clock a supervisor reads when it gives up for now, and the
// timers it waits by then. A nil field means the real one, and a nil Zone
// local time. Tests set them.
type Clock struct {
	Now       func() time.Time
	Zone      *time.Location
	NewTimer  func(d time.Duration) (c <-chan time.Time, stop func() bool)
	NewTicker func(d time.Duration) (c <-chan time.Time, stop func())
}

// withDefaults is c with the real clock, timers and local time in place
// of its nil fields.
func (c Clock) withDefaults() Clock {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Zone == nil {
		c.Zone = time.Local
	}
	if c.NewTimer == nil {
		c.NewTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
			t := time.NewTimer(d)
			return t.C, t.Stop
		}
	}
	if c.NewTicker == nil {
		c.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return c
}

// supervision is the state and bookkeeping both transports share: the
// connection's status and last error, the failed attempts and when the
// next one is due, the agent socket's state, and the timings that drive
// the supervisor. Session and perProcess embed it.
type supervision struct {
	Name string
	host string
	Logf func(format string, args ...any)

	pollInterval    time.Duration
	connectTimeout  time.Duration
	baseBackoff     time.Duration
	maxBackoff      time.Duration
	maxAuthFailures int

	// clock gives the time for the message of a supervisor that gave up
	// for now, and the timers of its wait.
	clock Clock

	// mu guards the fields below and the embedding transport's own state.
	mu      sync.Mutex
	status  Status
	lastErr string
	// failures counts the attempts that failed since the connection last
	// connected or was started; nextAttempt is when the supervisor tries
	// again, zero unless it is waiting to.
	failures    int
	nextAttempt time.Time
	// refusals counts the fatal failures since the connection last
	// connected; other failures leave it be. It outlives a supervisor, so
	// one that a Stop and a Start replace still makes a single attempt
	// before it waits.
	refusals int
	// retryNow is open while the supervisor waits authRetryEvery after
	// giving up for now; Start closes it to end the wait.
	retryNow chan struct{}
	// Once connected, agentPath is where the server has the agent socket,
	// or agentErr says why it has not.
	agentPath    string
	agentErr     string
	agentProblem AgentProblem
}

// init sets the defaults for a disconnected transport to host.
func (v *supervision) init(name, host string) {
	v.Name = name
	v.host = host
	v.Logf = func(string, ...any) {}
	v.pollInterval = 200 * time.Millisecond
	v.connectTimeout = 20 * time.Second
	v.baseBackoff = time.Second
	v.maxBackoff = 30 * time.Second
	v.maxAuthFailures = 3
	v.clock = Clock{}.withDefaults()
	v.status = StatusDisconnected
}

// Host is the ssh host the transport reaches.
func (v *supervision) Host() string { return v.host }

// Status returns the current connection state.
func (v *supervision) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

// LastError returns the most recent ssh error text.
func (v *supervision) LastError() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lastErr
}

// Health returns the connection's state, how many attempts have failed,
// when it tries again, and the agent socket's state.
func (v *supervision) Health() Health {
	v.mu.Lock()
	defer v.mu.Unlock()
	return Health{Status: v.status, LastError: v.lastErr, Failures: v.failures, NextAttempt: v.nextAttempt,
		Refused:   v.refusals >= v.maxAuthFailures,
		AgentPath: v.agentPath, AgentErr: v.agentErr, AgentProblem: v.agentProblem}
}

func (v *supervision) setStatus(st Status, errText string) {
	v.mu.Lock()
	v.status = st
	if errText != "" {
		v.lastErr = errText
	}
	v.nextAttempt = time.Time{}
	if st == StatusConnected {
		v.failures = 0
		v.refusals = 0
	}
	v.mu.Unlock()
	if errText != "" {
		v.Logf("%s: %s: %s", v.Name, st, errText)
	} else {
		v.Logf("%s: %s", v.Name, st)
	}
}

// retrying notes a failed attempt, that the transport is reconnecting and
// that the next attempt is in d, all at once, then logs it, so whoever the
// log tells sees them.
func (v *supervision) retrying(msg string, d time.Duration) {
	v.mu.Lock()
	v.status, v.lastErr = StatusReconnecting, msg
	v.failures++
	v.nextAttempt = v.clock.Now().Add(d)
	v.mu.Unlock()
	v.Logf("%s: %s: %s", v.Name, StatusReconnecting, msg)
}

// giveUp notes a failed attempt and stops trying: the error is one that
// trying again will not fix.
func (v *supervision) giveUp(msg string) {
	v.mu.Lock()
	v.failures++
	v.mu.Unlock()
	v.setStatus(StatusAuthFailed, msg)
}

// giveUpForNow notes a failed attempt and that the supervisor waits
// authRetryEvery before the next: the transport is auth failed, and msg
// gains a sentence with the time of the next attempt in the user's time
// zone. It returns the channel Start closes to end the wait.
func (v *supervision) giveUpForNow(msg string) <-chan struct{} {
	next := v.clock.Now().Add(authRetryEvery)
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	msg += " Nearhost tries again at " + next.In(v.clock.Zone).Format("15:04") + "."
	retryNow := make(chan struct{})
	v.mu.Lock()
	v.status, v.lastErr = StatusAuthFailed, msg
	v.failures++
	v.nextAttempt = next
	v.retryNow = retryNow
	v.mu.Unlock()
	v.Logf("%s: %s: %s", v.Name, StatusAuthFailed, msg)
	return retryNow
}

// retryAtOnceLocked ends the wait of a supervisor that gave up for now, so
// it makes one attempt at once, counting its failures afresh; a refusal
// leads straight back to the wait. The transport is connecting from here
// on, so a caller that would replace a session that gave up leaves this
// attempt alone. It does nothing to one that is not waiting so, which is
// how Starts during that attempt share it. It must be called with mu held.
func (v *supervision) retryAtOnceLocked() {
	if v.retryNow == nil {
		return
	}
	close(v.retryNow)
	v.retryNow = nil
	v.failures = 0
	v.status = StatusConnecting
	v.nextAttempt = time.Time{}
}

// attemptFailed notes a failed attempt with message msg. A fatal one counts
// as a refusal, and from the maxAuthFailures-th since the connection last
// connected on, the supervisor gives up for now (see giveUpForNow) and the
// result is the channel that ends its wait. Other failures cost the server
// no failed login: they neither count nor end the count, and are retried
// in backoff, as is a refusal before the limit. Then the transport is
// reconnecting, and the result is nil. Pass the result to waitToRetry.
func (v *supervision) attemptFailed(msg string, fatal bool, backoff time.Duration) (retryNow <-chan struct{}) {
	v.mu.Lock()
	if fatal {
		v.refusals++
	}
	refused := fatal && v.refusals >= v.maxAuthFailures
	v.mu.Unlock()
	if refused {
		return v.giveUpForNow(msg)
	}
	v.retrying(msg, backoff)
	return nil
}

// waitToRetry waits before the next attempt, after attemptFailed returned
// retryNow. With retryNow nil it waits *backoff and doubles it. Otherwise
// the supervisor gave up for now: it waits authRetryEvery, or until Start
// closes retryNow, or the wall clock passes the time it announced (see
// wallCheckEvery), and the next attempt starts the backoff afresh. Either
// way the count of refusals stands, so another refusal leads straight
// back to the wait. waitToRetry reports false when ctx ends first.
func (v *supervision) waitToRetry(ctx context.Context, retryNow <-chan struct{}, backoff *time.Duration) bool {
	if retryNow == nil {
		if !v.sleep(ctx, *backoff) {
			return false
		}
		*backoff = v.nextBackoff(*backoff)
		return true
	}
	defer func() {
		v.mu.Lock()
		if v.retryNow == retryNow {
			v.retryNow = nil
		}
		v.mu.Unlock()
	}()
	v.mu.Lock()
	due := v.nextAttempt
	v.mu.Unlock()
	timeUp, stopTimer := v.clock.NewTimer(authRetryEvery)
	defer stopTimer()
	wall, stopTicker := v.clock.NewTicker(wallCheckEvery)
	defer stopTicker()
	*backoff = v.baseBackoff
	for {
		select {
		case <-ctx.Done():
			return false
		case <-retryNow:
			return true
		case <-timeUp:
			return true
		case <-wall:
			// Round(0) drops the monotonic reading, which stops while the
			// computer sleeps, so the times compare by the wall clock.
			if !v.clock.Now().Round(0).Before(due) {
				return true
			}
		}
	}
}

// WaitConnected blocks until the transport is connected, has given up, or
// ctx ends.
func (v *supervision) WaitConnected(ctx context.Context) error {
	for {
		switch v.Status() {
		case StatusConnected:
			return nil
		case StatusAuthFailed:
			return fmt.Errorf("ssh %s: %s", v.host, v.LastError())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(v.pollInterval):
		}
	}
}

// sleep waits d or until ctx ends; it reports whether the full wait elapsed.
func (v *supervision) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (v *supervision) nextBackoff(b time.Duration) time.Duration {
	b *= 2
	if b > v.maxBackoff {
		b = v.maxBackoff
	}
	return b
}
