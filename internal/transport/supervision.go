package transport

import (
	"context"
	"fmt"
	"os/exec"
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

// authRetryEvery is how long a supervisor waits between attempts once
// maxAuthFailures attempts in a row have failed in a way that retrying
// seldom fixes, such as a refused login. A server whose sshd answers before
// its home directories are ready, or a laptop whose ssh agent wakes late,
// recovers by itself. ssh runs in BatchMode, so each attempt is one failed
// login on the server, and a strict fail2ban (five failures in ten
// minutes) must not ban the laptop for them.
const authRetryEvery = 15 * time.Minute

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

	// now and zone give the time for the message of a supervisor that gave
	// up for now, and newTimer starts its wait, which is authRetryEvery
	// long. Tests replace them.
	now      func() time.Time
	zone     *time.Location
	newTimer func(d time.Duration) (c <-chan time.Time, stop func() bool)

	// mu guards the fields below and the embedding transport's own state.
	mu      sync.Mutex
	status  Status
	lastErr string
	// failures counts the attempts that failed since the connection last
	// connected or was started; nextAttempt is when the supervisor tries
	// again, zero unless it is waiting to.
	failures    int
	nextAttempt time.Time
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
	v.now = time.Now
	v.zone = time.Local
	v.newTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		t := time.NewTimer(d)
		return t.C, t.Stop
	}
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
	v.nextAttempt = v.now().Add(d)
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
// gains the time of the next attempt in the user's time zone. It returns
// the channel Start closes to end the wait.
func (v *supervision) giveUpForNow(msg string) <-chan struct{} {
	next := v.now().Add(authRetryEvery)
	msg += "; trying again at " + next.In(v.zone).Format("15:04")
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
// it tries again at once and counts its failures afresh. It does nothing
// to one that is not waiting so. It must be called with mu held.
func (v *supervision) retryAtOnceLocked() {
	if v.retryNow == nil {
		return
	}
	close(v.retryNow)
	v.retryNow = nil
	v.failures = 0
}

// attemptFailed notes a failed attempt with message msg. A fatal one adds
// to *fatalRun, and from the maxAuthFailures-th in a row on the supervisor
// gives up for now (see giveUpForNow), and the result is the channel that
// ends its wait. Any other failure ends the run of fatal ones. Unless it
// gave up, the transport is reconnecting in backoff, and the result is nil.
// Pass the result to waitToRetry.
func (v *supervision) attemptFailed(msg string, fatal bool, fatalRun *int, backoff time.Duration) (retryNow <-chan struct{}) {
	if fatal {
		*fatalRun++
		if *fatalRun >= v.maxAuthFailures {
			return v.giveUpForNow(msg)
		}
	} else {
		*fatalRun = 0 // only consecutive fatal failures count
	}
	v.retrying(msg, backoff)
	return nil
}

// waitToRetry waits before the next attempt, after attemptFailed returned
// retryNow. With retryNow nil it waits *backoff and doubles it. Otherwise
// the supervisor gave up for now: it waits authRetryEvery, or until Start
// closes retryNow, and the next attempt starts the backoff afresh. One that
// Start asked for also starts the run of fatal failures afresh; one at the
// end of the wait does not, so another such failure leads straight back to
// the wait. waitToRetry reports false when ctx ends first.
func (v *supervision) waitToRetry(ctx context.Context, retryNow <-chan struct{}, fatalRun *int, backoff *time.Duration) bool {
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
	timeUp, stop := v.newTimer(authRetryEvery)
	defer stop()
	*backoff = v.baseBackoff
	select {
	case <-ctx.Done():
		return false
	case <-retryNow:
		*fatalRun = 0
	case <-timeUp:
	}
	return true
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
