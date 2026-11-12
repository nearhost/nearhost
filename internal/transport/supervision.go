package transport

import (
	"context"
	"fmt"
	"sync"
	"time"
)

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

	// mu guards the fields below and the embedding transport's own state.
	mu      sync.Mutex
	status  Status
	lastErr string
	// failures counts the attempts that failed since the connection last
	// connected or was started; nextAttempt is when the supervisor tries
	// again, zero unless it is waiting to.
	failures    int
	nextAttempt time.Time
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
	v.nextAttempt = time.Now().Add(d)
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

// attemptFailed notes a failed attempt with message msg. A fatal one adds
// to *fatalRun, and the maxAuthFailures-th in a row gives up; any other
// ends the run. Unless it gave up, the transport is reconnecting in backoff.
// It reports whether the supervisor goes on.
func (v *supervision) attemptFailed(msg string, fatal bool, fatalRun *int, backoff time.Duration) bool {
	if fatal {
		*fatalRun++
		if *fatalRun >= v.maxAuthFailures {
			v.giveUp(msg)
			return false
		}
	} else {
		*fatalRun = 0 // only consecutive fatal failures count
	}
	v.retrying(msg, backoff)
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
