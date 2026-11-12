package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// perProcess is a Transport that keeps no ControlMaster, for an ssh that has
// none (Windows' ssh.exe). Its link is a plain long-lived `ssh -N` per server,
// supervised as Session supervises its master; every other job runs an ssh of
// its own.
type perProcess struct {
	supervision
	SSHPath    string
	Env        []string
	Foreground bool

	lifecycle sync.Mutex // serialises Start and Stop; held while Stop waits for the supervisor
	// link is the running `ssh -N`, nil while there is none.
	link   *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}

	// agent is the socket forwarded for coding agents; nil for none.
	agent *AgentForward
}

var _ Transport = (*perProcess)(nil)

// newPerProcess returns an idle per-process transport for c. Call Start to
// connect.
func newPerProcess(c Config) *perProcess {
	p := &perProcess{
		SSHPath:    c.SSHPath,
		Env:        c.Env,
		Foreground: c.Foreground,
	}
	p.init(c.Name, c.Host)
	if c.Logf != nil {
		p.Logf = c.Logf
	}
	if p.SSHPath == "" {
		p.SSHPath = "ssh"
	}
	return p
}

// ConfigError is returned when the user's ssh configuration for a host sets
// options that would change every connection Nearhost makes to it, and that
// a per-process transport cannot switch off on the command line.
type ConfigError struct {
	Host    string
	Options []string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("%s's ssh configuration sets %s, which Nearhost can't keep out of its own connections on this system. "+
		"Move them to a Host entry Nearhost doesn't use.", e.Host, strings.Join(e.Options, ", "))
}

// configConflicts reads `ssh -G` output for host and returns a *ConfigError
// naming the options in it that a per-process connection cannot override:
// forwards the configuration adds (ClearAllForwardings=yes would clear
// Nearhost's own -L and -R with them), ClearAllForwardings itself, and
// ForkAfterAuthentication, which would put the link in the background where
// it cannot be supervised.
func configConflicts(host, config string) error {
	var options []string
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := strings.ToLower(fields[0])
		value := ""
		if len(fields) > 1 {
			value = strings.ToLower(fields[1])
		}
		conflict := false
		switch key {
		case "localforward", "remoteforward", "dynamicforward":
			conflict = true
		case "forkafterauthentication", "clearallforwardings":
			conflict = value == "yes"
		}
		if conflict && !slices.Contains(options, key) {
			options = append(options, key)
		}
	}
	if len(options) == 0 {
		return nil
	}
	return &ConfigError{Host: host, Options: options}
}

// Preflight has nothing to check: there is no control socket to fit.
func (p *perProcess) Preflight() error { return nil }

func (p *perProcess) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.SSHPath, args...)
	cmd.Env = append(os.Environ(), p.Env...)
	return cmd
}

// longLived is command for an ssh that stays up: the link, and SFTP. As with
// Session.longLived, it runs in a process group of its own unless the
// transport is a foreground one.
func (p *perProcess) longLived(ctx context.Context, args ...string) *exec.Cmd {
	cmd := p.command(ctx, args...)
	if !p.Foreground {
		ownGroup(cmd)
	}
	return cmd
}

// args are the arguments of every long-lived per-process ssh: extra between
// the options that keep the user's configuration from changing its job and
// the host. With no ControlMaster, the configuration's own sharing is
// switched off, and DEBUG1 makes ssh say when it has authenticated and set
// up each forward.
func (p *perProcess) args(extra ...string) []string {
	a := []string{
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "RemoteCommand=none",
		"-o", "LogLevel=DEBUG1",
	}
	a = append(a, extra...)
	return append(a, "--", p.host)
}

// Check verifies that ssh can reach the host without prompting, as
// Session.Check does.
func (p *perProcess) Check(ctx context.Context) error {
	return checkHost(p.command(ctx, checkArgs(p.host)...), p.host)
}

// OpenSFTP starts `ssh -s sftp` on a connection of its own. ctx bounds the
// handshake only; the subsystem lives until Close.
func (p *perProcess) OpenSFTP(ctx context.Context) (*SFTP, error) {
	cmd := p.longLived(context.Background(),
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"-s", "--", p.host, "sftp")
	return openSFTP(ctx, cmd, p.host)
}

// SetAgentForward chooses the socket forwarded for coding agents. Call it
// before Start.
func (p *perProcess) SetAgentForward(f AgentForward) {
	p.mu.Lock()
	p.agent = &f
	p.mu.Unlock()
}

// errForwardsNotYet is what the forward methods return until the
// per-process transport carries forwards.
var errForwardsNotYet = errors.New("not yet")

// AddForward is not supported yet.
func (p *perProcess) AddForward(ctx context.Context, localPort, remotePort int) error {
	return errForwardsNotYet
}

// CancelForward is not supported yet.
func (p *perProcess) CancelForward(ctx context.Context, localPort, remotePort int) error {
	return errForwardsNotYet
}

// Forwards lists no forwards yet.
func (p *perProcess) Forwards() [][2]int { return nil }

// ForwardStates is the connection state and, as yet, no forwards.
func (p *perProcess) ForwardStates() (Status, []ForwardState) { return p.Status(), nil }

// Start launches the supervisor. It is a no-op while a supervisor is
// running; after the supervisor has given up, Start launches a fresh one.
func (p *perProcess) Start() {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.cancel, p.done = cancel, done
	p.failures = 0
	go p.run(ctx, cancel, done)
}

// Stop ends the link and waits for the supervisor to exit.
func (p *perProcess) Stop() {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel, p.done = nil, nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	// Also covers a supervisor that already gave up (auth failed).
	if p.Status() != StatusDisconnected {
		p.setStatus(StatusDisconnected, "")
	}
}

// readConfig runs `ssh -G` for the host and returns a *ConfigError when its
// configuration sets what a per-process connection cannot override. It
// connects to nothing, so it runs in nearhost's own process group.
func (p *perProcess) readConfig(ctx context.Context) (conflict error, err error) {
	cmd := p.command(ctx, "-G", "--", p.host)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New("ssh -G: " + msg)
	}
	return configConflicts(p.host, string(out)), nil
}

// run supervises the link: it reads the host's configuration, launches
// `ssh -N`, marks the transport connected once ssh has authenticated, and
// relaunches it with backoff when it exits, until ctx ends or a failure
// that retrying cannot fix.
func (p *perProcess) run(ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	defer func() {
		// On self-exit, release the slot so a later Start can relaunch.
		// If Stop already took it, p.done differs and nothing is touched.
		p.mu.Lock()
		if p.done == done {
			p.cancel, p.done = nil, nil
		}
		p.mu.Unlock()
		cancel()
		close(done)
	}()
	backoff := p.baseBackoff
	authFails := 0
	for {
		if ctx.Err() != nil { // stopped before (re)launching the link
			p.setStatus(StatusDisconnected, "")
			return
		}
		p.setStatus(StatusConnecting, "")
		connected, msg, fatal, ok := p.connect(ctx)
		if !ok {
			return // the status is set
		}
		if connected {
			backoff = p.baseBackoff
			authFails = 0
		}
		// Fatal errors, and failures to start ssh at all, give up after
		// maxAuthFailures in a row.
		if !p.attemptFailed(msg, fatal, &authFails, backoff) {
			return
		}
		if !p.sleep(ctx, backoff) {
			p.setStatus(StatusDisconnected, "")
			return
		}
		backoff = p.nextBackoff(backoff)
	}
}

// connect makes one attempt: it reads the configuration, then runs the link
// until it exits. ok is false when the supervisor is to stop, with the final
// status set. Otherwise msg says why the attempt ended, fatal whether it
// counts towards giving up, and connected whether the link had connected.
func (p *perProcess) connect(ctx context.Context) (connected bool, msg string, fatal, ok bool) {
	conflict, err := p.readConfig(ctx)
	if ctx.Err() != nil {
		p.setStatus(StatusDisconnected, "")
		return false, "", false, false
	}
	if err != nil {
		// Usually a missing binary or a configuration ssh cannot parse.
		return false, err.Error(), true, true
	}
	if conflict != nil {
		p.giveUp(conflict.Error())
		return false, "", false, false
	}

	link := p.longLived(context.Background(), p.args("-N")...) // not tied to ctx: we stop it ourselves
	stderr := newLinkStderr()
	link.Stderr = stderr
	if err := link.Start(); err != nil {
		return false, "start ssh: " + err.Error(), true, true
	}
	p.mu.Lock()
	p.link = link
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.link = nil
		p.mu.Unlock()
	}()
	exited := make(chan struct{})
	go func() {
		link.Wait()
		close(exited)
	}()
	stop := func() {
		link.Process.Kill()
		<-exited
	}

	timeout := time.NewTimer(p.connectTimeout)
	defer timeout.Stop()
	select {
	case <-ctx.Done():
		stop()
		p.setStatus(StatusDisconnected, "")
		return false, "", false, false
	case <-timeout.C:
		// The link is alive but never authenticated. Kill it and retry.
		p.Logf("%s: connect timed out after %s", p.Name, p.connectTimeout)
		stop()
		return false, "connect timed out", false, true
	case <-stderr.authenticated:
		connected = true
		p.setStatus(StatusConnected, "")
		select {
		case <-ctx.Done():
			stop()
			p.setStatus(StatusDisconnected, "")
			return true, "", false, false
		case <-exited:
		}
	case <-exited:
	}

	lines := stderr.Lines()
	msg = exitMessage(lines)
	return connected, msg, isFatalSSHError(strings.Join(lines, "\n")), true
}

// exitMessage picks why the link exited from the last lines of its stderr:
// the last that is not ssh's DEBUG1 chatter or the byte counts it prints on
// the way out, else the last line, else "ssh exited".
func exitMessage(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !strings.HasPrefix(l, "debug") && !strings.HasPrefix(l, "Transferred:") && !strings.HasPrefix(l, "Bytes per second:") {
			return l
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return "ssh exited"
}

const (
	// stderrLines is how many of ssh's last non-empty stderr lines are kept.
	stderrLines = 20
	// maxStderrLine is the most of one line that is kept.
	maxStderrLine = 1024
)

// linkStderr takes a link's stderr for its whole life. It keeps the last
// stderrLines non-empty lines and closes authenticated at the first line
// that says ssh has logged in. It holds at most stderrLines lines of
// maxStderrLine bytes, however long ssh writes.
type linkStderr struct {
	authenticated chan struct{}

	mu      sync.Mutex
	partial []byte // the line being written, cut at maxStderrLine
	lines   []string
	authed  bool
}

func newLinkStderr() *linkStderr {
	return &linkStderr{authenticated: make(chan struct{})}
}

func (w *linkStderr) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if room := maxStderrLine - len(w.partial); room > 0 {
			w.partial = append(w.partial, chunk[:min(room, len(chunk))]...)
		}
		if i < 0 {
			break
		}
		w.endLine()
		b = b[i+1:]
	}
	return n, nil
}

// endLine files the line in partial.
func (w *linkStderr) endLine() {
	line := strings.TrimSpace(string(w.partial))
	w.partial = w.partial[:0]
	if line == "" {
		return
	}
	if !w.authed && (strings.Contains(line, "Authenticated to ") || strings.Contains(line, "Authentication succeeded")) {
		w.authed = true
		close(w.authenticated)
	}
	if len(w.lines) == stderrLines {
		copy(w.lines, w.lines[1:])
		w.lines = w.lines[:stderrLines-1]
	}
	w.lines = append(w.lines, line)
}

// Lines returns the last non-empty lines ssh wrote, oldest first, a final
// line without a newline included.
func (w *linkStderr) Lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]string(nil), w.lines...)
	if line := strings.TrimSpace(string(w.partial)); line != "" {
		out = append(out[max(0, len(out)-stderrLines+1):], line)
	}
	return out
}
