package transport

import (
	"context"
	"errors"
	"fmt"
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
