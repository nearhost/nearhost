package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status is the connection state of a Session.
type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusAuthFailed   Status = "auth failed"
)

// CheckError is returned when the non-interactive connectivity check fails.
type CheckError struct {
	Host   string
	Stderr string
}

func (e *CheckError) Error() string {
	return fmt.Sprintf("ssh %s failed: %s", e.Host, e.Stderr)
}

type forward struct {
	local, remote int
}

// Session supervises one OpenSSH ControlMaster connection to a server.
type Session struct {
	Name    string
	Host    string
	SSHPath string   // ssh binary, default "ssh"
	Env     []string // extra environment for ssh (tests)
	Logf    func(format string, args ...any)

	runDir          string
	pollInterval    time.Duration
	connectTimeout  time.Duration
	baseBackoff     time.Duration
	maxBackoff      time.Duration
	maxAuthFailures int

	lifecycle sync.Mutex // serialises Start and Stop; held while Stop waits for the supervisor
	mu        sync.Mutex
	status    Status
	lastErr   string
	launches  int
	forwards  map[string]forward
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewSession creates an idle session. Call Start to connect.
func NewSession(name, host, runDir string) *Session {
	return &Session{
		Name:            name,
		Host:            host,
		SSHPath:         "ssh",
		Logf:            func(string, ...any) {},
		runDir:          runDir,
		pollInterval:    200 * time.Millisecond,
		connectTimeout:  20 * time.Second,
		baseBackoff:     time.Second,
		maxBackoff:      30 * time.Second,
		maxAuthFailures: 3,
		status:          StatusDisconnected,
		forwards:        map[string]forward{},
	}
}

// SocketPath is the ControlMaster socket for this session. It is always a
// file directly inside the run directory.
func (s *Session) SocketPath() string {
	return filepath.Join(s.runDir, socketName(s.Name)+".sock")
}

// socketName returns name when it is a plain file name ssh takes literally.
// Anything else (path separators, "%" tokens, an empty name) is replaced by a
// hash so the socket cannot land outside the run directory.
func socketName(name string) string {
	plain := name != ""
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			plain = false
			break
		}
	}
	if plain {
		return name
	}
	return fmt.Sprintf("h-%x", sha256.Sum256([]byte(name)))[:18]
}

func (s *Session) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.SSHPath, args...)
	cmd.Env = append(os.Environ(), s.Env...)
	return cmd
}

// Check verifies that ssh can reach the host without prompting.
func (s *Session) Check(ctx context.Context) error {
	cmd := s.command(ctx, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", s.Host, "true")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return &CheckError{Host: s.Host, Stderr: msg}
	}
	return nil
}

// Status returns the current connection state.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// LastError returns the most recent ssh error text.
func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Launches counts how many times the master process has been started.
func (s *Session) Launches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launches
}

func (s *Session) setStatus(st Status, errText string) {
	s.mu.Lock()
	s.status = st
	if errText != "" {
		s.lastErr = errText
	}
	s.mu.Unlock()
	if errText != "" {
		s.Logf("%s: %s: %s", s.Name, st, errText)
	} else {
		s.Logf("%s: %s", s.Name, st)
	}
}

// Start launches the supervisor. It is a no-op while a supervisor is running;
// after the supervisor has given up, Start launches a fresh one.
func (s *Session) Start() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()
	go s.run(ctx, cancel, done)
}

// Stop ends the master connection and waits for the supervisor to exit.
func (s *Session) Stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	// Also covers a supervisor that already gave up (auth failed).
	if s.Status() != StatusDisconnected {
		s.setStatus(StatusDisconnected, "")
	}
}

// WaitConnected blocks until the session is connected, has failed
// authentication, or ctx ends.
func (s *Session) WaitConnected(ctx context.Context) error {
	for {
		switch s.Status() {
		case StatusConnected:
			return nil
		case StatusAuthFailed:
			return fmt.Errorf("ssh %s: %s", s.Host, s.LastError())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollInterval):
		}
	}
}

// isFatalSSHError reports errors that retrying cannot fix.
func isFatalSSHError(msg string) bool {
	for _, needle := range []string{
		"Permission denied",
		"Host key verification failed",
		"REMOTE HOST IDENTIFICATION HAS CHANGED",
		"Too many authentication failures",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func (s *Session) run(ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	defer func() {
		// On self-exit, release the slot so a later Start can relaunch.
		// If Stop already took it, s.done differs and nothing is touched.
		s.mu.Lock()
		if s.done == done {
			s.cancel, s.done = nil, nil
		}
		s.mu.Unlock()
		cancel()
		close(done)
	}()
	os.MkdirAll(s.runDir, 0o700)
	backoff := s.baseBackoff
	authFails := 0
	for {
		if ctx.Err() != nil { // stopped before (re)launching the master
			s.setStatus(StatusDisconnected, "")
			return
		}
		s.setStatus(StatusConnecting, "")
		s.cleanStaleSocket(ctx)
		master := s.command(context.Background(), // not tied to ctx: we stop it ourselves
			"-N", "-M", "-S", s.SocketPath(),
			"-o", "BatchMode=yes",
			"-o", "ExitOnForwardFailure=yes",
			"-o", "ServerAliveInterval=15",
			"-o", "ServerAliveCountMax=3",
			s.Host)
		var stderr bytes.Buffer
		master.Stderr = &stderr
		if err := master.Start(); err != nil {
			// Usually a missing binary, occasionally a passing fork failure:
			// retry, but give up after the same number of attempts.
			msg := "start ssh: " + err.Error()
			authFails++
			if authFails >= s.maxAuthFailures {
				s.setStatus(StatusAuthFailed, msg)
				return
			}
			s.setStatus(StatusReconnecting, msg)
			if !s.sleep(ctx, backoff) {
				s.setStatus(StatusDisconnected, "")
				return
			}
			backoff = s.nextBackoff(backoff)
			continue
		}
		s.mu.Lock()
		s.launches++
		s.mu.Unlock()
		exited := make(chan struct{})
		go func() {
			master.Wait()
			close(exited)
		}()

		if s.waitForSocket(ctx, exited) {
			s.setStatus(StatusConnected, "")
			backoff = s.baseBackoff
			authFails = 0
			s.reapplyForwards(ctx)
		} else if ctx.Err() == nil && !hasExited(exited) {
			// Connect timeout: the master is alive but never produced a
			// working socket. Kill it so the loop below retries.
			s.Logf("%s: connect timed out after %s", s.Name, s.connectTimeout)
			master.Process.Kill()
			<-exited
			authFails = 0
			s.setStatus(StatusReconnecting, "connect timed out")
			if !s.sleep(ctx, backoff) {
				s.setStatus(StatusDisconnected, "")
				return
			}
			backoff = s.nextBackoff(backoff)
			continue
		}

		select {
		case <-ctx.Done():
			s.exitMaster(master, exited)
			s.setStatus(StatusDisconnected, "")
			return
		case <-exited:
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "ssh exited"
			}
			if isFatalSSHError(msg) {
				authFails++
				if authFails >= s.maxAuthFailures {
					s.setStatus(StatusAuthFailed, msg)
					return
				}
			} else {
				authFails = 0 // only consecutive fatal failures count
			}
			s.setStatus(StatusReconnecting, msg)
		}

		if !s.sleep(ctx, backoff) {
			s.setStatus(StatusDisconnected, "")
			return
		}
		backoff = s.nextBackoff(backoff)
	}
}

func hasExited(exited <-chan struct{}) bool {
	select {
	case <-exited:
		return true
	default:
		return false
	}
}

// sleep waits d or until ctx ends; it reports whether the full wait elapsed.
func (s *Session) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Session) nextBackoff(b time.Duration) time.Duration {
	b *= 2
	if b > s.maxBackoff {
		b = s.maxBackoff
	}
	return b
}

// waitForSocket polls `ssh -O check` until the master answers, the process
// exits, the connect timeout passes, or ctx ends.
func (s *Session) waitForSocket(ctx context.Context, exited <-chan struct{}) bool {
	deadline := time.Now().Add(s.connectTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-exited:
			return false
		case <-time.After(s.pollInterval):
		}
		check := s.command(ctx, "-S", s.SocketPath(), "-O", "check", s.Host)
		if check.Run() == nil {
			return true
		}
	}
	return false
}

// cleanStaleSocket clears any socket left by a previous crashed run, exiting
// a master that is still alive on it.
func (s *Session) cleanStaleSocket(ctx context.Context) {
	if _, err := os.Stat(s.SocketPath()); err != nil {
		return
	}
	check := s.command(ctx, "-S", s.SocketPath(), "-O", "check", s.Host)
	if check.Run() == nil {
		// A master from a crashed run is still alive on this socket. Ask it
		// to exit so the new master is the one we supervise.
		exitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.command(exitCtx, "-S", s.SocketPath(), "-O", "exit", s.Host).Run()
	}
	os.Remove(s.SocketPath())
}

// exitMaster asks the master to exit politely, then kills it.
func (s *Session) exitMaster(master *exec.Cmd, exited <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.command(ctx, "-S", s.SocketPath(), "-O", "exit", s.Host).Run()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		master.Process.Kill()
		<-exited
	}
}

// reapplyForwards re-adds desired forwards after (re)connecting.
// Filled in by forward.go.
func (s *Session) reapplyForwards(ctx context.Context) {}
