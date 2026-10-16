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
	appliedGen    int // value of Session.launches when last applied; 0 if never
}

// Session supervises one OpenSSH ControlMaster connection to a server.
type Session struct {
	Name    string
	Host    string
	SSHPath string   // ssh binary, default "ssh"
	Env     []string // extra environment for ssh (tests)
	Logf    func(format string, args ...any)
	// Foreground keeps the master and the SFTP process in this process's
	// group, so the terminal's Ctrl-C reaches them as it reaches nearhost.
	// By default each runs in a process group of its own and only this
	// session ends it; that is for a caller that handles interrupts and
	// stops the session itself. A caller that simply dies on Ctrl-C sets
	// Foreground, or its master would outlive it.
	Foreground bool

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
	// failures counts the attempts that failed since the session last
	// connected or was started; nextAttempt is when the supervisor tries
	// again, zero unless it is waiting to.
	failures    int
	nextAttempt time.Time
	// fwdMu serialises forward changes and the re-apply pass after a
	// (re)connect. It is taken before mu and may be held across ssh calls.
	// Logf is called with it held, so a Logf callback must not call
	// AddForward or CancelForward; Status and ForwardStates are fine.
	fwdMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}

	// agent is the socket forwarded for coding agents; nil for none. Once
	// the master has connected, agentPath is where the server has it, or
	// agentErr says why it has not.
	agent     *AgentForward
	agentPath string
	agentErr  string
	// prepareAgent readies ~/.nearhost on the server and returns the path
	// of the socket called name there. Tests replace it.
	prepareAgent func(ctx context.Context, name string) (string, error)
}

// NewSession creates an idle session. Call Start to connect.
func NewSession(name, host, runDir string) *Session {
	s := &Session{
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
	s.prepareAgent = s.prepareAgentOverSFTP
	return s
}

// A Unix socket address holds 104 bytes on macOS (108 on Linux), the final NUL
// included, and OpenSSH binds a control socket at its path plus a 17-byte
// temporary suffix before renaming it. The macOS figure is used everywhere so
// that a path is judged the same on every system.
const (
	socketAddrSize  = 104
	sshBindSuffix   = 17
	maxSocketPath   = socketAddrSize - sshBindSuffix - 1
	hashedNameChars = 18 // "h-" and 16 hex digits
)

// SocketPath is the ControlMaster socket for this session. It is always a
// file directly inside the run directory. A name whose socket path would be
// too long for ssh to bind is replaced by its short hashed form; whether the
// result is usable at all (the run directory itself may be too long) is what
// CheckSocketPath reports.
func (s *Session) SocketPath() string {
	path := filepath.Join(s.runDir, socketName(s.Name)+".sock")
	if len(path) > maxSocketPath {
		if short := filepath.Join(s.runDir, hashedName(s.Name)+".sock"); len(short) < len(path) {
			return short
		}
	}
	return path
}

// CheckSocketPath reports whether ssh can bind this session's control socket.
// It fails when the run directory is so long that even the shortest socket
// name does not fit. Start refuses to launch ssh in that case.
func (s *Session) CheckSocketPath() error {
	path := s.SocketPath()
	if len(path) <= maxSocketPath {
		return nil
	}
	return fmt.Errorf("control socket path %s is too long for ssh: it is %d bytes and at most %d bytes fit; "+
		"the run directory %s is too long, set a shorter XDG_CONFIG_HOME", path, len(path), maxSocketPath, s.runDir)
}

// socketName returns name when it is a plain file name ssh takes literally.
// Anything else (path separators, "%" tokens, an empty name) is replaced by a
// hash so the socket cannot land outside the run directory. So is a name that
// has the form of a hashed name itself, in either letter case: every socket
// named like that then comes from a hash, and a server called that cannot be
// given the socket of the name the hash stands for.
func socketName(name string) string {
	plain := name != "" && !looksHashed(name)
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			plain = false
			break
		}
	}
	if plain {
		return name
	}
	return hashedName(name)
}

// looksHashed reports whether name has the form hashedName produces, in either
// letter case (the file system may not tell the two apart).
func looksHashed(name string) bool {
	if len(name) != hashedNameChars || name[0] != 'h' && name[0] != 'H' || name[1] != '-' {
		return false
	}
	for _, c := range name[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// hashedName is a short plain file name that stands for name.
func hashedName(name string) string {
	return fmt.Sprintf("h-%x", sha256.Sum256([]byte(name)))[:hashedNameChars]
}

func (s *Session) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.SSHPath, args...)
	cmd.Env = append(os.Environ(), s.Env...)
	return cmd
}

// longLived is command for the two ssh processes that stay up: the master and
// the SFTP subsystem. Unless the session is a foreground one they get a
// process group of their own, so a Ctrl-C in the terminal reaches nearhost
// only and the session ends them in its own time (Stop, or Close for SFTP).
// The check and the control commands are short and stay in nearhost's group.
func (s *Session) longLived(ctx context.Context, args ...string) *exec.Cmd {
	cmd := s.command(ctx, args...)
	if !s.Foreground {
		ownGroup(cmd)
	}
	return cmd
}

// The user's ssh_config applies to every ssh started here, and an option given
// on the command line wins over it. Each kind of invocation therefore switches
// off what would change its job: a RemoteCommand (ssh refuses to combine one
// with a command or subsystem of ours), forwards listed for the host, and
// connection sharing the user has set up.
//
// The control commands (-O check, forward, cancel, exit) take none of these.
// They only talk to the running master over its socket, and
// ClearAllForwardings would drop the very -L a forward or cancel names. They
// do not read the configuration at all (see control).

// control is command for one request to the running master over its socket:
// -O check, forward, cancel or exit. It reads no configuration file. The
// socket is named by -S and the host is only a label there, but a mux client
// sends the master every LocalForward and RemoteForward the configuration
// lists for the host along with the forward it was asked for.
func (s *Session) control(ctx context.Context, args ...string) *exec.Cmd {
	return s.command(ctx, append([]string{"-F", "/dev/null", "-S", s.SocketPath()}, args...)...)
}

// Check verifies that ssh can reach the host without prompting. It makes a
// connection of its own: one borrowed from a master the user already has open
// would say nothing about whether nearhost's master can log in.
func (s *Session) Check(ctx context.Context) error {
	cmd := s.command(ctx, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		s.Host, "true")
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

// Health is a session's state at one moment.
type Health struct {
	Status    Status
	LastError string
	// Failures counts the attempts that failed since the session last
	// connected or was started.
	Failures int
	// NextAttempt is when the session tries again; zero unless it is
	// reconnecting. A session that gave up (auth failed) does not try again
	// by itself.
	NextAttempt time.Time
	// AgentPath is the socket agents on the server reach nearhost on, while
	// it is forwarded; AgentErr says why it is not, once that was tried and
	// failed. Both are empty for a session that forwards none.
	AgentPath, AgentErr string
}

// Health returns the session's state, how many attempts have failed, and
// when it tries again.
func (s *Session) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Health{Status: s.status, LastError: s.lastErr, Failures: s.failures, NextAttempt: s.nextAttempt,
		AgentPath: s.agentPath, AgentErr: s.agentErr}
}

func (s *Session) setStatus(st Status, errText string) {
	s.mu.Lock()
	s.status = st
	if errText != "" {
		s.lastErr = errText
	}
	s.nextAttempt = time.Time{}
	if st == StatusConnected {
		s.failures = 0
	}
	s.mu.Unlock()
	if errText != "" {
		s.Logf("%s: %s: %s", s.Name, st, errText)
	} else {
		s.Logf("%s: %s", s.Name, st)
	}
}

// Start launches the supervisor. It is a no-op while a supervisor is running;
// after the supervisor has given up, Start launches a fresh one. When the
// control socket path is too long (see CheckSocketPath) it launches nothing
// and the session is at once in the state of a supervisor that gave up, with
// that error as its last one.
func (s *Session) Start() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	running := s.cancel != nil
	s.mu.Unlock()
	if running {
		return
	}
	// ssh would fail on such a path every time, and say so only after it
	// had connected. Give up here, as the supervisor does on an error that
	// retrying cannot fix.
	if err := s.CheckSocketPath(); err != nil {
		s.setStatus(StatusAuthFailed, err.Error())
		return
	}
	s.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.failures = 0
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
		master := s.longLived(context.Background(), // not tied to ctx: we stop it ourselves
			"-N", "-M", "-S", s.SocketPath(),
			"-o", "BatchMode=yes",
			"-o", "ExitOnForwardFailure=yes",
			"-o", "ServerAliveInterval=15",
			"-o", "ServerAliveCountMax=3",
			// ControlPersist would put the master in the background, where
			// it cannot be supervised. The forwards nearhost wants are asked
			// for later over the control socket, so clearing the ones in the
			// user's configuration loses nothing and keeps a port that is
			// already taken from ending the master.
			"-o", "ControlPersist=no",
			"-o", "ClearAllForwardings=yes",
			"-o", "RemoteCommand=none",
			s.Host)
		var stderr bytes.Buffer
		master.Stderr = &stderr
		if err := master.Start(); err != nil {
			// Usually a missing binary, occasionally a passing fork failure:
			// retry, but give up after the same number of attempts.
			msg := "start ssh: " + err.Error()
			authFails++
			if authFails >= s.maxAuthFailures {
				s.giveUp(msg)
				return
			}
			s.retrying(msg, backoff)
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
			if s.markConnected(ctx, exited) {
				backoff = s.baseBackoff
				authFails = 0
				s.applyAgent(ctx)
			} else if ctx.Err() == nil && !hasExited(exited) {
				// The master stopped answering during the re-apply pass
				// without exiting. Kill it so the code below retries.
				master.Process.Kill()
			}
		} else if ctx.Err() == nil && !hasExited(exited) {
			// Connect timeout: the master is alive but never produced a
			// working socket. Kill it so the loop below retries.
			s.Logf("%s: connect timed out after %s", s.Name, s.connectTimeout)
			master.Process.Kill()
			<-exited
			authFails = 0
			s.retrying("connect timed out", backoff)
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
			s.mu.Lock()
			s.agentPath = "" // the server dropped it with the master
			s.mu.Unlock()
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "ssh exited"
			}
			if isFatalSSHError(msg) {
				authFails++
				if authFails >= s.maxAuthFailures {
					s.giveUp(msg)
					return
				}
			} else {
				authFails = 0 // only consecutive fatal failures count
			}
			s.retrying(msg, backoff)
		}

		if !s.sleep(ctx, backoff) {
			s.setStatus(StatusDisconnected, "")
			return
		}
		backoff = s.nextBackoff(backoff)
	}
}

// retrying notes a failed attempt, that the session is reconnecting and
// that the next attempt is in d, all at once, then logs it, so whoever the
// log tells sees them.
func (s *Session) retrying(msg string, d time.Duration) {
	s.mu.Lock()
	s.status, s.lastErr = StatusReconnecting, msg
	s.failures++
	s.nextAttempt = time.Now().Add(d)
	s.mu.Unlock()
	s.Logf("%s: %s: %s", s.Name, StatusReconnecting, msg)
}

// giveUp notes a failed attempt and stops trying: the error is one that
// trying again will not fix.
func (s *Session) giveUp(msg string) {
	s.mu.Lock()
	s.failures++
	s.mu.Unlock()
	s.setStatus(StatusAuthFailed, msg)
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
		check := s.control(ctx, "-O", "check", s.Host)
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
	check := s.control(ctx, "-O", "check", s.Host)
	if check.Run() == nil {
		// A master from a crashed run is still alive on this socket. Ask it
		// to exit so the new master is the one we supervise.
		exitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.control(exitCtx, "-O", "exit", s.Host).Run()
	}
	os.Remove(s.SocketPath())
}

// exitMaster asks the master to exit politely, then kills it.
func (s *Session) exitMaster(master *exec.Cmd, exited <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.cancelAgent(ctx)
	s.control(ctx, "-O", "exit", s.Host).Run()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		master.Process.Kill()
		<-exited
	}
}
