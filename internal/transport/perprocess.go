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
	// live counts the long-lived ssh processes started and not yet reaped.
	live int

	// forwards are the desired forwards by forwardKey, each carried by an
	// ssh process of its own while the link is up.
	forwards map[string]*perProcessForward
	// linkCtx is the context of the link that is up, from the start of
	// the re-apply pass until the forwards are closed with it; nil
	// otherwise. It ends when the link exits.
	linkCtx context.Context
	// fwdMu serialises forward changes, the re-apply pass after the link
	// gets ready and the closing of the forwards when it exits, as
	// Session's does. It is taken before mu and held while forwards start.
	// Logf is called with it held, so a Logf callback must not call
	// AddForward or CancelForward; Status and ForwardStates are fine.
	fwdMu sync.Mutex
	// launchSlots holds a token for each forward that is starting, at most
	// maxForwardLaunches.
	launchSlots chan struct{}

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
		SSHPath:     c.SSHPath,
		Env:         c.Env,
		Foreground:  c.Foreground,
		forwards:    map[string]*perProcessForward{},
		launchSlots: make(chan struct{}, maxForwardLaunches),
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
		"-o", "PermitLocalCommand=no",
		"-o", "Tunnel=no",
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
// connects to nothing, so it runs in nearhost's own process group. fatal
// says whether a failure counts towards giving up: ssh that did not start
// at all does, and ssh that ran and failed does when what it said does.
func (p *perProcess) readConfig(ctx context.Context) (conflict error, fatal bool, err error) {
	cmd := p.command(ctx, "-G", "--", p.host)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		var exit *exec.ExitError
		return nil, !errors.As(err, &exit) || isFatalSSHError(msg), errors.New("ssh -G: " + msg)
	}
	return configConflicts(p.host, string(out)), false, nil
}

// run supervises the link: it reads the host's configuration, launches
// `ssh -N`, marks the transport connected once ssh has authenticated and
// forwarded the agent socket and the forwards are re-applied, closes the
// forwards when the link exits, and relaunches it with backoff when it exits,
// until ctx ends or a failure that retrying cannot fix. A link that exits
// because the server refused the agent socket, or because ssh could not
// parse its forward, is relaunched at once without it, as the same attempt;
// the next full reconnect tries it again.
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
		goOn := p.attemptFailed(end.msg, end.fatal, &authFails, backoff)
		p.endForwards() // after the status has left connected
		if !goOn {
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
	// agentRefused is whether the link exited, before it connected,
	// because the server refused the agent socket's forward or ssh could
	// not parse it.
	agentRefused bool
}

// connect makes one attempt: it reads the configuration, readies the agent
// socket on the server when withAgent is set, then runs the link until it
// exits. ok is false when the supervisor is to stop, with the final status
// set.
func (p *perProcess) connect(ctx context.Context, withAgent bool) (end linkEnd, ok bool) {
	conflict, fatal, err := p.readConfig(ctx)
	if ctx.Err() != nil {
		p.setStatus(StatusDisconnected, "")
		return linkEnd{}, false
	}
	if err != nil {
		return linkEnd{msg: err.Error(), fatal: fatal}, true
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

	stderr := newLinkStderr(remote)
	link, err := p.startProc(p.args(args...), stderr)
	if err != nil {
		return linkEnd{msg: "start ssh: " + err.Error(), fatal: true}, true
	}
	p.mu.Lock()
	p.link = link.cmd
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.link = nil
		p.agentPath = "" // the server drops it with the link
		p.mu.Unlock()
	}()

	// The link has connectTimeout to get ready. With the agent socket to
	// forward, it has connectTimeout to authenticate, then as long again
	// for the server to answer the forward; a server that never does
	// counts as one that refused.
	timeout := time.NewTimer(p.connectTimeout)
	defer timeout.Stop()
	var authenticated <-chan struct{}
	if remote != "" {
		authenticated = stderr.authenticated
	}
	answering := false
wait:
	for {
		select {
		case <-ctx.Done():
			link.stop()
			p.setStatus(StatusDisconnected, "")
			return linkEnd{}, false
		case <-authenticated:
			authenticated = nil
			answering = true
			timeout.Reset(p.connectTimeout)
		case <-timeout.C:
			link.stop()
			if answering {
				p.setAgentErr(fmt.Errorf("the server never answered the forward of %s", remote), AgentRefused)
				return linkEnd{agentRefused: true}, true
			}
			// The link is alive but never got ready. Kill it and retry.
			p.Logf("%s: connect timed out after %s", p.Name, p.connectTimeout)
			return linkEnd{msg: "connect timed out"}, true
		case <-stderr.ready:
			end.connected = true
			if remote != "" {
				p.mu.Lock()
				p.agentPath, p.agentErr, p.agentProblem = remote, "", AgentOK
				p.mu.Unlock()
			}
			if !p.linkUp(ctx, link, remote) {
				return end, false
			}
			break wait
		case <-link.exited:
			break wait
		}
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
	// ssh rejects a -R it cannot parse before it connects: a local socket
	// path holding a colon, as a Windows path does. That is no fault of
	// the server, and the link goes on without the agent socket.
	if bad := stderr.BadSpec(); bad != "" && !end.connected {
		p.setAgentErr(fmt.Errorf("ssh cannot forward %s: %s", remote, bad), AgentNotReady)
		return linkEnd{agentRefused: true}, true
	}
	lines := stderr.Lines()
	end.msg = exitMessage(lines)
	end.fatal = isFatalSSHError(strings.Join(lines, "\n"))
	return end, true
}

// linkUp runs while the link that has just got ready stays up: it
// re-applies the forwards, reports the transport connected, and waits for
// the link to exit. It reports false when ctx ended first, with the link
// stopped, the status set and the forwards closed. Otherwise the caller
// closes the forwards, once the status says why the link went.
func (p *perProcess) linkUp(ctx context.Context, link *sshProc, remote string) bool {
	linkCtx, cancelLink := context.WithCancel(ctx)
	defer cancelLink()
	go func() {
		select {
		case <-link.exited:
			cancelLink()
		case <-linkCtx.Done():
		}
	}()
	if p.markConnected(linkCtx) {
		if remote != "" {
			p.Logf("%s: agents reach nearhost at %s", p.Name, remote)
		}
		select {
		case <-ctx.Done():
		case <-link.exited:
		}
	}
	if ctx.Err() != nil {
		link.stop()
		p.setStatus(StatusDisconnected, "")
		p.endForwards()
		return false
	}
	return true
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

// sshProc is a long-lived ssh that has started: the link, or a forward's.
type sshProc struct {
	cmd    *exec.Cmd
	stderr *linkStderr
	// exited closes once the process has been reaped.
	exited chan struct{}
}

// procWaitDelay bounds how long reaping a killed link or forward waits for
// its stderr to close. A ProxyCommand or ProxyJump child that inherited the
// pipe would otherwise hold Wait, and with it CancelForward and Stop, until
// it exits.
const procWaitDelay = 5 * time.Second

// startProc starts a long-lived ssh with args, its stderr going to stderr,
// and reaps it when it exits; running counts it until then.
func (p *perProcess) startProc(args []string, stderr *linkStderr) (*sshProc, error) {
	cmd := p.longLived(context.Background(), args...) // not tied to a context: we stop it ourselves
	cmd.Stderr = stderr
	cmd.WaitDelay = procWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.live++
	p.mu.Unlock()
	proc := &sshProc{cmd: cmd, stderr: stderr, exited: make(chan struct{})}
	go func() {
		cmd.Wait()
		p.mu.Lock()
		p.live--
		p.mu.Unlock()
		close(proc.exited)
	}()
	return proc, nil
}

// stop kills the process and waits until it has been reaped.
func (sp *sshProc) stop() {
	sp.cmd.Process.Kill()
	<-sp.exited
}

// running counts the long-lived ssh processes that have not been reaped.
func (p *perProcess) running() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live
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

// linkStderr takes a long-lived ssh's stderr for its whole life. It keeps
// the last stderrLines non-empty lines and closes authenticated at the
// first line that says ssh has logged in. ready closes at the line that
// says the process has done its job: for a link that forwards the agent
// socket, that the server forwards it, and the line that says it refused is
// kept; for a link without, ready is authenticated; for a forward's
// process, that it listens. It holds at most stderrLines lines of
// maxStderrLine bytes, however long ssh writes.
type linkStderr struct {
	authenticated chan struct{}
	ready         chan struct{}
	// readyOn is what the line that closes ready contains, "" when ready
	// is authenticated.
	readyOn string
	// agent is the agent socket's path on the server, "" for none.
	agent string

	mu      sync.Mutex
	partial []byte // the line being written, cut at maxStderrLine
	lines   []string
	authed  bool
	isReady bool
	refusal string
	badSpec string
}

// newLinkStderr returns a linkStderr for a link that forwards the agent
// socket to agent on the server, or none for "".
func newLinkStderr(agent string) *linkStderr {
	w := &linkStderr{authenticated: make(chan struct{}), agent: agent}
	w.ready = w.authenticated
	if agent != "" {
		w.ready = make(chan struct{})
		w.readyOn = "remote forward success for: listen " + agent
	}
	return w
}

// newForwardStderr returns a linkStderr for the process of the forward
// from local, ready once ssh listens there.
func newForwardStderr(local int) *linkStderr {
	return &linkStderr{
		authenticated: make(chan struct{}),
		ready:         make(chan struct{}),
		readyOn:       fmt.Sprintf("Local forwarding listening on 127.0.0.1 port %d.", local),
	}
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
	if w.readyOn != "" && !w.isReady && strings.Contains(line, w.readyOn) {
		w.isReady = true
		close(w.ready)
	}
	if w.agent != "" && w.refusal == "" && strings.Contains(line, "remote port forwarding failed for listen path "+w.agent) {
		w.refusal = line
	}
	if w.agent != "" && w.badSpec == "" && strings.Contains(line, "Bad remote forwarding specification") {
		w.badSpec = line
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

// BadSpec is the line in which ssh said it could not parse the agent
// socket's -R, or "".
func (w *linkStderr) BadSpec() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.badSpec
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
