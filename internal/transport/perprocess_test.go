package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// perProcessFake is a per-process transport wired to the fake ssh, with the
// files the fake reports to.
type perProcessFake struct {
	*perProcess
	log, pidLog, dropDir string
}

// newTestPerProcess returns a per-process transport on the fake ssh, which
// logs its invocations, its plain processes' pids, and drops a link whose
// pid is written into dropDir.
func newTestPerProcess(t *testing.T, env ...string) *perProcessFake {
	t.Helper()
	dir := t.TempDir()
	f := &perProcessFake{
		log:     filepath.Join(dir, "ssh.log"),
		pidLog:  filepath.Join(dir, "pid.log"),
		dropDir: filepath.Join(dir, "drop"),
	}
	if err := os.Mkdir(f.dropDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.perProcess = newPerProcess(Config{
		Name:    "mini",
		Host:    "mini",
		SSHPath: testutil.FakeSSH(t),
		Env: append([]string{
			"FAKE_SSH_LOG=" + f.log,
			"FAKE_SSH_PID_LOG=" + f.pidLog,
			"FAKE_SSH_DROP_DIR=" + f.dropDir,
		}, env...),
	})
	f.pollInterval = 20 * time.Millisecond
	f.baseBackoff = 50 * time.Millisecond
	f.maxBackoff = 200 * time.Millisecond
	t.Cleanup(f.Stop)
	return f
}

// linkPids returns the pids of the plain processes the fake started, in
// order.
func (f *perProcessFake) linkPids(t *testing.T) []int {
	t.Helper()
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(readLog(t, f.pidLog)), "\n") {
		first, _, _ := strings.Cut(line, " ")
		if pid, err := strconv.Atoi(first); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitPerProcessConnected(t *testing.T, p *perProcessFake) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.WaitConnected(ctx); err != nil {
		t.Fatalf("WaitConnected: %v (last error %q)", err, p.LastError())
	}
}

func TestPerProcessConnects(t *testing.T) {
	p := newTestPerProcess(t)
	p.Start()
	waitPerProcessConnected(t, p)
	if p.Status() != StatusConnected {
		t.Fatalf("status = %s", p.Status())
	}
	lines := logLines(t, p.log)
	if len(lines) != 1 {
		t.Fatalf("want one ssh invocation, got %q", lines)
	}
	argv := " " + lines[0] + " "
	for _, want := range []string{" -N ", " -o ControlMaster=no ", " -o LogLevel=DEBUG1 ", " -o ExitOnForwardFailure=yes ", " -- mini "} {
		if !strings.Contains(argv, want) {
			t.Errorf("the link's argv %q lacks %q", lines[0], want)
		}
	}
	for _, unwanted := range []string{" -S ", " -M ", " -O "} {
		if strings.Contains(argv, unwanted) {
			t.Errorf("the link's argv %q has %q", lines[0], unwanted)
		}
	}
}

func TestPerProcessReconnectsAfterTheLinkDrops(t *testing.T) {
	p := newTestPerProcess(t)
	var mu sync.Mutex
	var statuses []Status
	p.Logf = func(format string, args ...any) {
		if len(args) >= 2 {
			if st, ok := args[1].(Status); ok {
				mu.Lock()
				statuses = append(statuses, st)
				mu.Unlock()
			}
		}
	}
	p.Start()
	waitPerProcessConnected(t, p)
	first := p.linkPids(t)
	if len(first) != 1 {
		t.Fatalf("want one link pid, got %v", first)
	}
	if err := os.WriteFile(filepath.Join(p.dropDir, strconv.Itoa(first[0])), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return len(p.linkPids(t)) == 2 && p.Status() == StatusConnected
	}, "a second link")
	mu.Lock()
	defer mu.Unlock()
	reconnecting := false
	for _, st := range statuses {
		if st == StatusReconnecting {
			reconnecting = true
		}
	}
	if !reconnecting {
		t.Fatalf("the status never was %s: %v", StatusReconnecting, statuses)
	}
	if !strings.Contains(p.LastError(), "Broken pipe") {
		t.Fatalf("LastError = %q, want the drop's message", p.LastError())
	}
}

func TestPerProcessGivesUpOnAuthFailure(t *testing.T) {
	p := newTestPerProcess(t, "FAKE_SSH_AUTH_FAIL=1")
	p.Start()
	waitFor(t, 5*time.Second, func() bool { return p.Status() == StatusAuthFailed }, "auth failed status")
	if !strings.Contains(p.LastError(), "Permission denied") {
		t.Fatalf("LastError = %q", p.LastError())
	}
	if h := p.Health(); h.Failures != 3 {
		t.Fatalf("Failures = %d, want 3", h.Failures)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.WaitConnected(ctx); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("WaitConnected should surface the auth error, got %v", err)
	}
}

func TestPerProcessRefusesAHostWhoseConfigForwards(t *testing.T) {
	for _, tc := range []struct{ config, option string }{
		{"localforward 5432 127.0.0.1:5432", "localforward"},
		{"forkafterauthentication yes", "forkafterauthentication"},
		{"clearallforwardings yes", "clearallforwardings"},
		{"remoteforward 9000 127.0.0.1:9000", "remoteforward"},
		{"dynamicforward 1080", "dynamicforward"},
	} {
		t.Run(tc.option, func(t *testing.T) {
			p := newTestPerProcess(t, "FAKE_SSH_G_CONFIG=clearallforwardings no\nforkafterauthentication no\n"+tc.config)
			p.Start()
			waitFor(t, 5*time.Second, func() bool { return p.Status() == StatusAuthFailed }, "auth failed status")
			if h := p.Health(); h.Failures != 1 {
				t.Fatalf("Failures = %d, want 1", h.Failures)
			}
			msg := p.LastError()
			if !strings.Contains(msg, "mini") || !strings.Contains(msg, tc.option) {
				t.Fatalf("LastError = %q, want it to name mini and %s", msg, tc.option)
			}
			if strings.Contains(readLog(t, p.log), "-N") {
				t.Fatalf("a link was launched: %q", readLog(t, p.log))
			}
		})
	}
}

func TestPerProcessConfigErrorListsEveryOption(t *testing.T) {
	cfg := "hostname mini\nlocalforward 1 127.0.0.1:1\nlocalforward 2 127.0.0.1:2\ndynamicforward 1080\nclearallforwardings no\n"
	err := configConflicts("mini", cfg)
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v, want a *ConfigError", err)
	}
	want := "mini's ssh configuration sets localforward, dynamicforward, which Nearhost can't keep out of its own connections on this system. Move them to a Host entry Nearhost doesn't use."
	if ce.Error() != want {
		t.Fatalf("Error() = %q\nwant      %q", ce.Error(), want)
	}
	if err := configConflicts("mini", "hostname mini\nclearallforwardings no\nforkafterauthentication no\n"); err != nil {
		t.Fatalf("a plain configuration was refused: %v", err)
	}
}

func TestPerProcessOpenSFTPWorksOnItsOwnConnection(t *testing.T) {
	p := newTestPerProcess(t, "FAKE_SSH_SFTP_SERVER="+testutil.SFTPServer(t))
	getwd := func() {
		t.Helper()
		f, err := p.OpenSFTP(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Client.Getwd(); err != nil {
			t.Fatal(err)
		}
	}
	getwd()
	p.Start()
	waitPerProcessConnected(t, p)
	getwd()
	var sftpLines int
	for _, line := range logLines(t, p.log) {
		if !strings.HasSuffix(line, " sftp") {
			continue
		}
		sftpLines++
		argv := " " + line + " "
		if !strings.Contains(argv, " -s ") {
			t.Errorf("the SFTP argv %q lacks -s", line)
		}
		if strings.Contains(argv, " -S ") || strings.Contains(argv, " -N ") {
			t.Errorf("the SFTP argv %q has -S or -N", line)
		}
	}
	if sftpLines != 2 {
		t.Fatalf("want two SFTP invocations, got %d in %q", sftpLines, readLog(t, p.log))
	}
}

func TestPerProcessCheck(t *testing.T) {
	p := newTestPerProcess(t)
	if err := p.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	refused := newTestPerProcess(t, "FAKE_SSH_CHECK_REFUSE=This account is currently not available.")
	err := refused.Check(context.Background())
	var ce *CheckError
	if !errors.As(err, &ce) || !strings.Contains(ce.Stderr, "not available") {
		t.Fatalf("got %v, want a *CheckError with the server's message", err)
	}
}

func TestPerProcessStopEndsTheLink(t *testing.T) {
	p := newTestPerProcess(t)
	p.Start()
	waitPerProcessConnected(t, p)
	pids := p.linkPids(t)
	if len(pids) != 1 {
		t.Fatalf("want one link pid, got %v", pids)
	}
	p.Stop()
	if p.Status() != StatusDisconnected {
		t.Fatalf("status after Stop = %s", p.Status())
	}
	waitFor(t, 5*time.Second, func() bool { return processGone(pids[0]) }, "the link to exit")
}

func TestPerProcessPreflightNeedsNoSocket(t *testing.T) {
	p := newTestPerProcess(t)
	if err := p.Preflight(); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestPerProcessArgs(t *testing.T) {
	p := newTestPerProcess(t)
	got := strings.Join(p.args("-N"), " ")
	want := "-o BatchMode=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 " +
		"-o ControlMaster=no -o ControlPath=none -o RemoteCommand=none -o LogLevel=DEBUG1 -N -- mini"
	if got != want {
		t.Fatalf("args = %q\nwant   %q", got, want)
	}
}

func TestPerProcessStderrKeepsTheLastLines(t *testing.T) {
	w := newLinkStderr("")
	for i := range 30 {
		w.Write([]byte("debug1: line " + strconv.Itoa(i) + "\n\n"))
	}
	w.Write([]byte("Authenticated to mini ([127.0.0.1]:22) using \"publickey\".\n"))
	select {
	case <-w.authenticated:
	default:
		t.Fatal("authenticated was not closed")
	}
	// A line written in pieces, and one far longer than is kept.
	w.Write([]byte("client_loop: send "))
	w.Write([]byte("disconnect: Broken pipe\n"))
	w.Write([]byte(strings.Repeat("x", 10*maxStderrLine) + "\n"))
	lines := w.Lines()
	if len(lines) != stderrLines {
		t.Fatalf("kept %d lines, want %d", len(lines), stderrLines)
	}
	if lines[len(lines)-2] != "client_loop: send disconnect: Broken pipe" {
		t.Fatalf("lines end %q", lines[len(lines)-2:])
	}
	if len(lines[len(lines)-1]) != maxStderrLine {
		t.Fatalf("a long line was kept at %d bytes, want %d", len(lines[len(lines)-1]), maxStderrLine)
	}
}

func TestPerProcessConnectTimeoutKillsTheLinkAndRetries(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := newTestPerProcess(t, "FAKE_SSH_LINK_HOLD="+hold)
	p.connectTimeout = 200 * time.Millisecond
	p.Start()
	waitFor(t, 5*time.Second, func() bool { return p.LastError() == "connect timed out" }, "a connect timeout")
	if st := p.Status(); st == StatusConnected || st == StatusAuthFailed {
		t.Fatalf("status = %s while the link is held", st)
	}
	os.Remove(hold)
	waitPerProcessConnected(t, p)
}

// At DEBUG1 ssh goes on writing after the line that says why it left: the
// byte counts, then debug1 lines. The error is the line before them.
func TestPerProcessExitMessageSkipsTheDebugTrailer(t *testing.T) {
	w := newLinkStderr("")
	w.Write([]byte("debug1: client_input_channel_req: channel 0 rtype exit-status reply 0\n" +
		"Connection to mini closed by remote host.\n" +
		"Transferred: sent 2352, received 3016 bytes, in 4.1 seconds\n" +
		"Bytes per second: sent 573.4, received 735.3\n" +
		"debug1: Exit status -1\n"))
	if got := exitMessage(w.Lines()); got != "Connection to mini closed by remote host." {
		t.Fatalf("exit message = %q", got)
	}
	if got := exitMessage([]string{"debug1: one", "debug1: two"}); got != "debug1: two" {
		t.Fatalf("with only debug lines, exit message = %q, want the last", got)
	}
	if got := exitMessage(nil); got != "ssh exited" {
		t.Fatalf("with no lines, exit message = %q", got)
	}
}

// withPerProcessAgent forwards the agent socket on p, with prepareAgent
// answering remote, or err.
func withPerProcessAgent(p *perProcessFake, remote string, err error) {
	p.SetAgentForward(AgentForward{Name: "k3f9q2.sock", Local: "/tmp/a.sock"})
	p.prepareAgent = func(ctx context.Context, f AgentForward) (string, error) {
		return remote, err
	}
}

// logged collects what p logs.
func logged(p *perProcessFake) func() string {
	var mu sync.Mutex
	var b strings.Builder
	p.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		b.WriteString(fmt.Sprintf(format, args...) + "\n")
	}
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return b.String()
	}
}

func TestPerProcessForwardsTheAgentSocket(t *testing.T) {
	p := newTestPerProcess(t)
	withPerProcessAgent(p, agentRemote, nil)
	logs := logged(p)
	p.Start()
	waitPerProcessConnected(t, p)
	lines := logLines(t, p.log)
	if len(lines) != 1 || !strings.Contains(lines[0], " -R "+agentRemote+":/tmp/a.sock ") {
		t.Fatalf("want one link with -R %s:/tmp/a.sock, got %q", agentRemote, lines)
	}
	if h := p.Health(); h.AgentPath != agentRemote || h.AgentErr != "" || h.AgentProblem != AgentOK {
		t.Fatalf("Health = %+v, want the agent at %s", h, agentRemote)
	}
	if !strings.Contains(logs(), "agents reach nearhost at "+agentRemote) {
		t.Fatalf("log %q lacks the agent line", logs())
	}
}

func TestPerProcessARefusedAgentForwardLeavesTheLinkUp(t *testing.T) {
	p := newTestPerProcess(t, "FAKE_SSH_RFORWARD_FAIL=1")
	withPerProcessAgent(p, agentRemote, nil)
	var mu sync.Mutex
	var statuses []Status
	p.Logf = func(format string, args ...any) {
		if len(args) >= 2 {
			if st, ok := args[1].(Status); ok {
				mu.Lock()
				statuses = append(statuses, st)
				mu.Unlock()
			}
		}
	}
	p.Start()
	waitPerProcessConnected(t, p)
	lines := logLines(t, p.log)
	if len(lines) != 2 || !strings.Contains(lines[0], " -R ") || strings.Contains(lines[1], " -R ") {
		t.Fatalf("want a link with -R, then one without, got %q", lines)
	}
	h := p.Health()
	if h.AgentProblem != AgentRefused || !strings.Contains(h.AgentErr, "remote port forwarding failed for listen path "+agentRemote) {
		t.Fatalf("Health = %+v, want the refusal", h)
	}
	if h.AgentPath != "" {
		t.Fatalf("AgentPath = %q after a refusal", h.AgentPath)
	}
	if h.Failures != 0 {
		t.Fatalf("Failures = %d, want 0", h.Failures)
	}
	pids := p.linkPids(t)
	time.Sleep(time.Second)
	if again := p.linkPids(t); len(pids) != 1 || len(again) != 1 || again[0] != pids[0] {
		t.Fatalf("link pids went from %v to %v, want one that stays", pids, again)
	}
	if p.Status() != StatusConnected {
		t.Fatalf("status = %s", p.Status())
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(statuses, StatusReconnecting) {
		t.Fatalf("the status went %v", statuses)
	}
}

func TestPerProcessARefusedAgentForwardTooLong(t *testing.T) {
	long := "/home/" + strings.Repeat("d", 100) + "/.nearhost/k3f9q2.sock"
	p := newTestPerProcess(t, "FAKE_SSH_RFORWARD_FAIL=1")
	withPerProcessAgent(p, long, nil)
	p.Start()
	waitPerProcessConnected(t, p)
	if h := p.Health(); h.AgentProblem != AgentTooLong || !strings.Contains(h.AgentErr, "longer than a Unix socket path") {
		t.Fatalf("Health = %+v, want the path too long", h)
	}
}

func TestPerProcessRetriesTheAgentForwardOnTheNextReconnect(t *testing.T) {
	p := newTestPerProcess(t)
	var calls atomic.Int32
	p.SetAgentForward(AgentForward{Name: "k3f9q2.sock", Local: "/tmp/a.sock"})
	p.prepareAgent = func(ctx context.Context, f AgentForward) (string, error) {
		calls.Add(1)
		return agentRemote, nil
	}
	p.Start()
	waitPerProcessConnected(t, p)
	first := p.linkPids(t)
	if err := os.WriteFile(filepath.Join(p.dropDir, strconv.Itoa(first[0])), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return len(p.linkPids(t)) == 2 && p.Status() == StatusConnected
	}, "a second link")
	if calls.Load() != 2 {
		t.Fatalf("prepareAgent ran %d times, want 2", calls.Load())
	}
	lines := logLines(t, p.log)
	if !strings.Contains(lines[len(lines)-1], " -R "+agentRemote+":/tmp/a.sock ") {
		t.Fatalf("the second link %q lacks the agent forward", lines[len(lines)-1])
	}
	if p.Health().AgentPath != agentRemote {
		t.Fatalf("AgentPath = %q", p.Health().AgentPath)
	}
}

func TestPerProcessAgentOnAServerThatIsThisMachine(t *testing.T) {
	p := newTestPerProcess(t)
	withPerProcessAgent(p, "", ErrThisMachine)
	logs := logged(p)
	p.Start()
	waitPerProcessConnected(t, p)
	if log := readLog(t, p.log); strings.Contains(log, " -R ") {
		t.Fatalf("the link forwards the agent socket: %q", log)
	}
	if h := p.Health(); h.AgentPath != "" || h.AgentErr != "" || h.AgentProblem != AgentOK {
		t.Fatalf("Health = %+v, want no agent state", h)
	}
	if !strings.Contains(logs(), "agents here reach nearhost on this machine's own socket") {
		t.Fatalf("log %q lacks the this-machine line", logs())
	}
}

func TestPerProcessAgentNotReady(t *testing.T) {
	p := newTestPerProcess(t)
	withPerProcessAgent(p, "", errors.New("make /home/dev/.nearhost: permission denied"))
	logs := logged(p)
	p.Start()
	waitPerProcessConnected(t, p)
	if log := readLog(t, p.log); strings.Contains(log, " -R ") {
		t.Fatalf("the link forwards the agent socket: %q", log)
	}
	if h := p.Health(); h.AgentProblem != AgentNotReady || !strings.Contains(h.AgentErr, "permission denied") || h.AgentPath != "" {
		t.Fatalf("Health = %+v, want not ready", h)
	}
	if !strings.Contains(logs(), "agents unavailable: make /home/dev/.nearhost: permission denied") {
		t.Fatalf("log %q lacks the unavailable line", logs())
	}
}

// OpenSSH writes the stream forward's outcome with a port of -2 after the
// path, and newer releases put the function's name before it.
func TestPerProcessStderrSeesTheAgentForwardAsSSHWritesIt(t *testing.T) {
	w := newLinkStderr(agentRemote)
	w.Write([]byte("Authenticated to mini ([127.0.0.1]:22) using \"publickey\".\n"))
	select {
	case <-w.ready:
		t.Fatal("ready before the forward")
	default:
	}
	w.Write([]byte("debug1: client_input_global_request: remote forward success for: listen " + agentRemote + ":-2, connect /tmp/a.sock:-2\n"))
	select {
	case <-w.ready:
	default:
		t.Fatal("ready was not closed")
	}

	refused := newLinkStderr(agentRemote)
	refused.Write([]byte("Error: remote port forwarding failed for listen path " + agentRemote + "\ndebug1: Exit status 255\n"))
	if got := refused.Refusal(); got != "Error: remote port forwarding failed for listen path "+agentRemote {
		t.Fatalf("Refusal = %q", got)
	}
}

// forwardPid is the pid of the process that carries the forward, or 0 for
// none.
func (f *perProcessFake) forwardPid(local, remote int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fw, ok := f.forwards[forwardKey(local, remote)]; ok && fw.proc != nil {
		return fw.proc.cmd.Process.Pid
	}
	return 0
}

// linkPid is the pid of the running link, or 0 for none.
func (f *perProcessFake) linkPid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.link == nil {
		return 0
	}
	return f.link.Process.Pid
}

// drop makes the fake's plain process pid exit as a dropped connection does.
func (f *perProcessFake) drop(t *testing.T, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dropDir, strconv.Itoa(pid)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	port, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// portFree reports whether nothing listens on 127.0.0.1:port.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func forwardStatesString(st Status, fs []ForwardState) string {
	return fmt.Sprintf("%s %+v", st, fs)
}

func TestPerProcessAddsAForwardInItsOwnProcess(t *testing.T) {
	p := newTestPerProcess(t)
	p.Start()
	waitPerProcessConnected(t, p)
	local := freePort(t)
	if err := p.AddForward(t.Context(), local, 80); err != nil {
		t.Fatalf("AddForward: %v", err)
	}
	st, fs := p.ForwardStates()
	if st != StatusConnected || len(fs) != 1 || fs[0] != (ForwardState{Local: local, Remote: 80, Applied: true}) {
		t.Fatalf("ForwardStates = %s, want it applied", forwardStatesString(st, fs))
	}
	if got := p.Forwards(); len(got) != 1 || got[0] != [2]int{local, 80} {
		t.Fatalf("Forwards = %v", got)
	}
	lines := logLines(t, p.log)
	if len(lines) != 2 {
		t.Fatalf("want the link and the forward, got %q", lines)
	}
	if strings.Contains(lines[0], " -L ") {
		t.Fatalf("the link %q carries a forward", lines[0])
	}
	argv := " " + lines[1] + " "
	if !strings.Contains(argv, " -N ") || !strings.Contains(argv, " -L "+forwardSpec(local, 80)+" ") {
		t.Fatalf("the forward's argv %q lacks -N or -L %s", lines[1], forwardSpec(local, 80))
	}
	if p.forwardPid(local, 80) == 0 || p.forwardPid(local, 80) == p.linkPid() {
		t.Fatalf("forward pid %d, link pid %d", p.forwardPid(local, 80), p.linkPid())
	}
	if p.running() != 2 {
		t.Fatalf("running = %d, want 2", p.running())
	}
}

func TestPerProcessCancelEndsOnlyThatForward(t *testing.T) {
	p := newTestPerProcess(t)
	p.Start()
	waitPerProcessConnected(t, p)
	first, second := freePort(t), freePort(t)
	for _, local := range []int{first, second} {
		if err := p.AddForward(t.Context(), local, 80); err != nil {
			t.Fatalf("AddForward %d: %v", local, err)
		}
	}
	firstPid, secondPid := p.forwardPid(first, 80), p.forwardPid(second, 80)
	if err := p.CancelForward(t.Context(), first, 80); err != nil {
		t.Fatalf("CancelForward: %v", err)
	}
	if !processGone(firstPid) {
		t.Fatalf("the cancelled forward's process %d is still running", firstPid)
	}
	if !portFree(first) {
		t.Fatalf("port %d still listens", first)
	}
	if got := p.forwardPid(second, 80); got != secondPid {
		t.Fatalf("the other forward's pid went from %d to %d", secondPid, got)
	}
	st, fs := p.ForwardStates()
	if st != StatusConnected || len(fs) != 1 || fs[0] != (ForwardState{Local: second, Remote: 80, Applied: true}) {
		t.Fatalf("ForwardStates = %s, want only the second, applied", forwardStatesString(st, fs))
	}
	if p.running() != 2 {
		t.Fatalf("running = %d, want 2", p.running())
	}
}

func TestPerProcessForwardOnATakenPortIsNotApplied(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	p := newTestPerProcess(t, "FAKE_SSH_LINK_HOLD="+hold)
	p.Start()
	waitPerProcessConnected(t, p)
	taken, free := freePort(t), freePort(t)
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", taken))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	err = p.AddForward(t.Context(), taken, 80)
	if err == nil || !strings.Contains(err.Error(), "Address already in use") {
		t.Fatalf("AddForward on a taken port = %v", err)
	}
	if got := p.Forwards(); len(got) != 0 {
		t.Fatalf("a failed forward is tracked: %v", got)
	}
	if err := p.AddForward(t.Context(), free, 80); err != nil {
		t.Fatalf("AddForward: %v", err)
	}

	// Hold the next link while the forward on the taken port is added.
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.drop(t, p.linkPid())
	waitFor(t, 5*time.Second, func() bool { return p.Status() != StatusConnected }, "the link to drop")
	if err := p.AddForward(t.Context(), taken, 80); err != nil {
		t.Fatalf("AddForward while disconnected: %v", err)
	}
	os.Remove(hold)
	waitPerProcessConnected(t, p)
	st, fs := p.ForwardStates()
	want := []ForwardState{{Local: taken, Remote: 80}, {Local: free, Remote: 80, Applied: true}}
	if taken > free {
		want[0], want[1] = want[1], want[0]
	}
	if st != StatusConnected || !slices.Equal(fs, want) {
		t.Fatalf("ForwardStates = %s, want %+v", forwardStatesString(st, fs), want)
	}
}

func TestPerProcessRelaunchesOnlyTheForwardThatDied(t *testing.T) {
	p := newTestPerProcess(t)
	var mu sync.Mutex
	var sawLost bool
	p.Logf = func(format string, args ...any) {
		st, fs := p.ForwardStates()
		for _, f := range fs {
			if st == StatusConnected && !f.Applied {
				mu.Lock()
				sawLost = true
				mu.Unlock()
			}
		}
	}
	p.Start()
	waitPerProcessConnected(t, p)
	dies, stays := freePort(t), freePort(t)
	for _, local := range []int{dies, stays} {
		if err := p.AddForward(t.Context(), local, 80); err != nil {
			t.Fatalf("AddForward %d: %v", local, err)
		}
	}
	diesPid, staysPid, linkPid := p.forwardPid(dies, 80), p.forwardPid(stays, 80), p.linkPid()
	p.drop(t, diesPid)
	waitFor(t, 5*time.Second, func() bool {
		pid := p.forwardPid(dies, 80)
		return pid != 0 && pid != diesPid
	}, "the forward to be relaunched")
	st, fs := p.ForwardStates()
	if st != StatusConnected || len(fs) != 2 || !fs[0].Applied || !fs[1].Applied {
		t.Fatalf("ForwardStates = %s, want both applied", forwardStatesString(st, fs))
	}
	mu.Lock()
	lost := sawLost
	mu.Unlock()
	if !lost {
		t.Fatal("the dead forward was never seen unapplied")
	}
	if p.forwardPid(stays, 80) != staysPid || p.linkPid() != linkPid {
		t.Fatalf("the other forward went %d to %d, the link %d to %d",
			staysPid, p.forwardPid(stays, 80), linkPid, p.linkPid())
	}
	if !processGone(diesPid) {
		t.Fatalf("the dead forward's process %d is still running", diesPid)
	}
}

func TestPerProcessReappliesForwardsAfterReconnect(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	p := newTestPerProcess(t, "FAKE_SSH_LINK_HOLD="+hold)
	var mu sync.Mutex
	var unsettled []string
	connects := 0
	p.Logf = func(format string, args ...any) {
		st, fs := p.ForwardStates()
		if st != StatusConnected {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if len(args) >= 2 && args[1] == StatusConnected {
			connects++
		}
		for _, f := range fs {
			if !f.Applied {
				unsettled = append(unsettled, forwardStatesString(st, fs))
			}
		}
	}
	first, second := freePort(t), freePort(t)
	if err := p.AddForward(t.Context(), first, 80); err != nil {
		t.Fatalf("AddForward before Start: %v", err)
	}
	if st, fs := p.ForwardStates(); len(fs) != 1 || fs[0].Applied {
		t.Fatalf("before Start, ForwardStates = %s", forwardStatesString(st, fs))
	}
	p.Start()
	waitPerProcessConnected(t, p)
	firstPid := p.forwardPid(first, 80)
	if firstPid == 0 {
		t.Fatal("the forward added before Start has no process")
	}

	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.drop(t, p.linkPid())
	waitFor(t, 5*time.Second, func() bool { return p.Status() != StatusConnected }, "the link to drop")
	if err := p.AddForward(t.Context(), second, 80); err != nil {
		t.Fatalf("AddForward while disconnected: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return processGone(firstPid) }, "the forward to end with the link")
	if st, fs := p.ForwardStates(); len(fs) != 2 || fs[0].Applied || fs[1].Applied {
		t.Fatalf("while reconnecting, ForwardStates = %s", forwardStatesString(st, fs))
	}
	os.Remove(hold)
	waitPerProcessConnected(t, p)
	st, fs := p.ForwardStates()
	if st != StatusConnected || len(fs) != 2 || !fs[0].Applied || !fs[1].Applied {
		t.Fatalf("ForwardStates = %s, want both applied", forwardStatesString(st, fs))
	}
	mu.Lock()
	defer mu.Unlock()
	if connects != 2 {
		t.Fatalf("saw %d connects, want 2", connects)
	}
	if len(unsettled) > 0 {
		t.Fatalf("connected with forwards unsettled: %q", unsettled)
	}
}

func TestPerProcessStopLeavesNoProcess(t *testing.T) {
	t.Run("connecting", func(t *testing.T) {
		hold := filepath.Join(t.TempDir(), "hold")
		if err := os.WriteFile(hold, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		p := newTestPerProcess(t, "FAKE_SSH_LINK_HOLD="+hold)
		p.Start()
		waitFor(t, 5*time.Second, func() bool { return p.linkPid() != 0 }, "the link to start")
		pid := p.linkPid()
		p.Stop()
		if p.running() != 0 {
			t.Fatalf("running = %d after Stop", p.running())
		}
		if !processGone(pid) {
			t.Fatalf("the link %d outlived Stop", pid)
		}
	})
	t.Run("with forwards", func(t *testing.T) {
		p := newTestPerProcess(t)
		p.Start()
		waitPerProcessConnected(t, p)
		for range 2 {
			if err := p.AddForward(t.Context(), freePort(t), 80); err != nil {
				t.Fatal(err)
			}
		}
		pids := p.linkPids(t)
		if len(pids) != 3 {
			t.Fatalf("want three processes, got %v", pids)
		}
		p.Stop()
		if p.running() != 0 {
			t.Fatalf("running = %d after Stop", p.running())
		}
		for _, pid := range pids {
			if !processGone(pid) {
				t.Fatalf("process %d outlived Stop", pid)
			}
		}
		if st, fs := p.ForwardStates(); st != StatusDisconnected || len(fs) != 2 || fs[0].Applied || fs[1].Applied {
			t.Fatalf("after Stop, ForwardStates = %s", forwardStatesString(st, fs))
		}
	})
}

func TestPerProcessAnAgentForwardNeverAnsweredCountsAsRefused(t *testing.T) {
	p := newTestPerProcess(t, "FAKE_SSH_RFORWARD_SILENT=1")
	p.connectTimeout = 300 * time.Millisecond
	withPerProcessAgent(p, agentRemote, nil)
	p.Start()
	waitPerProcessConnected(t, p)
	lines := logLines(t, p.log)
	if len(lines) != 2 || !strings.Contains(lines[0], " -R ") || strings.Contains(lines[1], " -R ") {
		t.Fatalf("want a link with -R, then one without, got %q", lines)
	}
	h := p.Health()
	if h.AgentProblem != AgentRefused || h.AgentErr != "the server never answered the forward of "+agentRemote {
		t.Fatalf("Health = %+v, want the silence as a refusal", h)
	}
	if h.Failures != 0 {
		t.Fatalf("Failures = %d, want 0", h.Failures)
	}
	if p.running() != 1 {
		t.Fatalf("running = %d, want only the second link", p.running())
	}
}
