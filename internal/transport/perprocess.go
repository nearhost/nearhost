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
	// prepareAgent readies ~/.nearhost on the server and returns the path
	// of the forward's socket there. Tests replace it.
	prepareAgent func(ctx context.Context, f AgentForward) (string, error)
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
	p.prepareAgent = func(ctx context.Context, f AgentForward) (string, error) {
		return prepareAgentWith(ctx, p.OpenSFTP, p.Name, f)
	}
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
// `ssh -N`, marks the transport connected once ssh has authenticated and
// forwarded the agent socket, and relaunches it with backoff when it exits,
// until ctx ends or a failure that retrying cannot fix. A link that exits
// because the server refused the agent socket is relaunched at once without
// it, as the same attempt; the next full reconnect tries it again.
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
	withAgent := true
	for {
		if ctx.Err() != nil { // stopped before (re)launching the link
			p.setStatus(StatusDisconnected, "")
			return
		}
		if withAgent { // a relaunch without the agent is still connecting
			p.setStatus(StatusConnecting, "")
		}
		end, ok := p.connect(ctx, withAgent)
		if !ok {
			return // the status is set
		}
		withAgent = !end.agentRefused
		if end.agentRefused {
			continue
		}
		if end.connected {
			backoff = p.baseBackoff
			authFails = 0
		}
		// Fatal errors, and failures to start ssh at all, give up after
		// maxAuthFailures in a row.
		if !p.attemptFailed(end.msg, end.fatal, &authFails, backoff) {
			return
		}
		if !p.sleep(ctx, backoff) {
			p.setStatus(StatusDisconnected, "")
			return
		}
		backoff = p.nextBackoff(backoff)
	}
}

// linkEnd is how one launch of the link ended.
type linkEnd struct {
	// connected is whether the link had connected.
	connected bool
	// msg says why the link exited, and fatal whether that counts towards
	// giving up.
	msg   string
	fatal bool
	// agentRefused is whether the link exited because the server refused
	// the agent socket's forward, before it connected.
	agentRefused bool
}

// connect makes one attempt: it reads the configuration, readies the agent
// socket on the server when withAgent is set, then runs the link until it
// exits. ok is false when the supervisor is to stop, with the final status
// set.
func (p *perProcess) connect(ctx context.Context, withAgent bool) (end linkEnd, ok bool) {
	conflict, err := p.readConfig(ctx)
	if ctx.Err() != nil {
		p.setStatus(StatusDisconnected, "")
		return linkEnd{}, false
	}
	if err != nil {
		// Usually a missing binary or a configuration ssh cannot parse.
		return linkEnd{msg: err.Error(), fatal: true}, true
	}
	if conflict != nil {
		p.giveUp(conflict.Error())
		return linkEnd{}, false
	}

	args := []string{"-N"}
	remote := ""
	if withAgent {
		var local string
		remote, local = p.readyAgent(ctx)
		if ctx.Err() != nil {
			p.setStatus(StatusDisconnected, "")
			return linkEnd{}, false
		}
		if remote != "" {
			args = append(args, "-R", remote+":"+local)
		}
	}

	link := p.longLived(context.Background(), p.args(args...)...) // not tied to ctx: we stop it ourselves
	stderr := newLinkStderr(remote)
	link.Stderr = stderr
	if err := link.Start(); err != nil {
		return linkEnd{msg: "start ssh: " + err.Error(), fatal: true}, true
	}
	p.mu.Lock()
	p.link = link
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.link = nil
		p.agentPath = "" // the server drops it with the link
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
		return linkEnd{}, false
	case <-timeout.C:
		// The link is alive but never got ready. Kill it and retry.
		p.Logf("%s: connect timed out after %s", p.Name, p.connectTimeout)
		stop()
		return linkEnd{msg: "connect timed out"}, true
	case <-stderr.ready:
		end.connected = true
		if remote != "" {
			p.mu.Lock()
			p.agentPath, p.agentErr, p.agentProblem = remote, "", AgentOK
			p.mu.Unlock()
		}
		p.setStatus(StatusConnected, "")
		if remote != "" {
			p.Logf("%s: agents reach nearhost at %s", p.Name, remote)
		}
		select {
		case <-ctx.Done():
			stop()
			p.setStatus(StatusDisconnected, "")
			return end, false
		case <-exited:
		}
	case <-exited:
	}

	if refusal := stderr.Refusal(); refusal != "" && !end.connected {
		err := fmt.Errorf("the server refused to forward %s: %s", remote, refusal)
		problem := AgentRefused
		if len(remote) > maxRemoteSocket {
			problem = AgentTooLong
			err = errSocketTooLong(remote)
		}
		p.setAgentErr(err, problem)
		return linkEnd{agentRefused: true}, true
	}
	lines := stderr.Lines()
	end.msg = exitMessage(lines)
	end.fatal = isFatalSSHError(strings.Join(lines, "\n"))
	return end, true
}

// readyAgent readies the agent socket on the server for the link about to
// launch, and returns its path there and the local socket it reaches, or
// "" when the link is to forward none: there is no agent forward, the
// server is this machine, or readying failed, which Health then says.
func (p *perProcess) readyAgent(ctx context.Context) (remote, local string) {
	p.mu.Lock()
	f := p.agent
	p.mu.Unlock()
	if f == nil {
		return "", ""
	}
	remote, err := p.prepareAgent(ctx, *f)
	if ctx.Err() != nil {
		return "", "" // stopping: what this pass found says nothing
	}
	if errors.Is(err, ErrThisMachine) {
		p.mu.Lock()
		p.agentPath, p.agentErr, p.agentProblem = "", "", AgentOK
		p.mu.Unlock()
		p.Logf("%s: agents here reach nearhost on this machine's own socket", p.Name)
		return "", ""
	}
	if err != nil {
		p.setAgentErr(err, AgentNotReady)
		return "", ""
	}
	return remote, f.Local
}

// setAgentErr notes and logs why agents on the server cannot reach
// nearhost.
func (p *perProcess) setAgentErr(err error, problem AgentProblem) {
	p.mu.Lock()
	p.agentPath, p.agentErr, p.agentProblem = "", err.Error(), problem
	p.mu.Unlock()
	p.Logf("%s: agents unavailable: %v", p.Name, err)
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
// that says ssh has logged in. With an agent socket to forward, ready closes
// at the line that says the server forwards it, and the line that says it
// refused is kept; without, ready is authenticated. It holds at most
// stderrLines lines of maxStderrLine bytes, however long ssh writes.
type linkStderr struct {
	authenticated chan struct{}
	ready         chan struct{}
	// agent is the agent socket's path on the server, "" for none.
	agent string

	mu        sync.Mutex
	partial   []byte // the line being written, cut at maxStderrLine
	lines     []string
	authed    bool
	forwarded bool
	refusal   string
}

// newLinkStderr returns a linkStderr for a link that forwards the agent
// socket to agent on the server, or none for "".
func newLinkStderr(agent string) *linkStderr {
	w := &linkStderr{authenticated: make(chan struct{}), agent: agent}
	w.ready = w.authenticated
	if agent != "" {
		w.ready = make(chan struct{})
	}
	return w
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
	if w.agent != "" {
		switch {
		case !w.forwarded && strings.Contains(line, "remote forward success for: listen "+w.agent):
			w.forwarded = true
			close(w.ready)
		case w.refusal == "" && strings.Contains(line, "remote port forwarding failed for listen path "+w.agent):
			w.refusal = line
		}
	}
	if len(w.lines) == stderrLines {
		copy(w.lines, w.lines[1:])
		w.lines = w.lines[:stderrLines-1]
	}
	w.lines = append(w.lines, line)
}

// Refusal is the line in which ssh said the server refused to forward the
// agent socket, or "".
func (w *linkStderr) Refusal() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.refusal
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
