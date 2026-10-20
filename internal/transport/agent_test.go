package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const agentRemote = "/home/dev/.nearhost/k3f9q2.sock"

// withAgent gives s an agent forward whose preparation is counted and
// answers agentRemote, or err.
func withAgent(s *Session, err error) *atomic.Int32 {
	return withAgentAt(s, agentRemote, err)
}

// withAgentAt is withAgent with the socket at remote.
func withAgentAt(s *Session, remote string, err error) *atomic.Int32 {
	var n atomic.Int32
	s.prepareAgent = func(ctx context.Context, f AgentForward) (string, error) {
		n.Add(1)
		if f.Name != "k3f9q2.sock" {
			return "", errors.New("wrong name " + f.Name)
		}
		return remote, err
	}
	s.SetAgentForward(AgentForward{Name: "k3f9q2.sock", Local: "/run/nh/agent-mini.sock"})
	return &n
}

func TestTheAgentSocketIsForwardedOnceConnected(t *testing.T) {
	s, log := newFakeSession(t)
	withAgent(s, nil)
	s.Start()
	waitFor(t, 3*time.Second, func() bool { return s.Health().AgentPath == agentRemote }, "the agent forward")
	if !strings.Contains(readLog(t, log), "-O forward -R "+agentRemote+":/run/nh/agent-mini.sock mini") {
		t.Fatalf("no -R forward in:\n%s", readLog(t, log))
	}
	s.Stop()
	if !strings.Contains(readLog(t, log), "-O cancel -R "+agentRemote+":/run/nh/agent-mini.sock mini") {
		t.Fatalf("Stop did not cancel it:\n%s", readLog(t, log))
	}
	if h := s.Health(); h.AgentPath != "" {
		t.Fatalf("after Stop: %+v", h)
	}
}

func TestTheAgentSocketIsForwardedAgainAfterAReconnect(t *testing.T) {
	s, log := newFakeSession(t, "FAKE_SSH_FLAKY_ONCE=0.3")
	n := withAgent(s, nil)
	s.Start()
	waitFor(t, 5*time.Second, func() bool { return n.Load() >= 2 && s.Health().AgentPath == agentRemote }, "a second forward")
	if got := strings.Count(readLog(t, log), "-O forward -R "); got < 2 {
		t.Fatalf("%d -R forwards in:\n%s", got, readLog(t, log))
	}
}

func TestARefusedAgentForwardLeavesTheMasterUp(t *testing.T) {
	refusal := "remote port forwarding failed for listen path " + agentRemote
	s, _ := newFakeSession(t, "FAKE_SSH_RFORWARD_FAIL="+refusal)
	withAgent(s, nil)
	s.Start()
	waitFor(t, 3*time.Second, func() bool { return s.Health().AgentErr != "" }, "the refusal")
	h := s.Health()
	if !strings.Contains(h.AgentErr, refusal) || h.AgentPath != "" {
		t.Fatalf("health %+v", h)
	}
	time.Sleep(200 * time.Millisecond)
	if st := s.Status(); st != StatusConnected {
		t.Fatalf("the master went: %s", st)
	}
	if err := s.AddForward(context.Background(), 18080, 8080); err != nil {
		t.Fatalf("a project forward after the refusal: %v", err)
	}
}

func TestAFailedPreparationForwardsNothing(t *testing.T) {
	s, log := newFakeSession(t)
	withAgent(s, errors.New("make /home/dev/.nearhost: permission denied"))
	s.Start()
	waitFor(t, 3*time.Second, func() bool { return s.Health().AgentErr != "" }, "the failure")
	if h := s.Health(); !strings.Contains(h.AgentErr, "permission denied") {
		t.Fatalf("health %+v", h)
	}
	if strings.Contains(readLog(t, log), " -R ") {
		t.Fatalf("forwarded anyway:\n%s", readLog(t, log))
	}
}

func TestASessionWithoutAnAgentForwardForwardsNone(t *testing.T) {
	s, log := newFakeSession(t)
	s.Start()
	waitFor(t, 3*time.Second, func() bool { return s.Status() == StatusConnected }, "connected")
	s.Stop()
	if strings.Contains(readLog(t, log), " -R ") {
		t.Fatalf("an -R without an agent forward:\n%s", readLog(t, log))
	}
}

func TestPrepareAgentDirMakesThePrivateDirectoryAndRemovesOnlyItsOwnSocket(t *testing.T) {
	client, home := inProcessSFTP(t)
	other := filepath.Join(home, AgentDir, "zzzzzz.sock")
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, "k3f9q2.sock"), nil, 0o600)

	sock, err := PrepareAgentDir(client, home, "k3f9q2.sock", "", "")
	if err != nil || sock != filepath.Join(home, AgentDir, "k3f9q2.sock") {
		t.Fatalf("%q %v", sock, err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("its own socket is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another install's socket went: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(home, AgentDir)); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	// Again, with nothing to remove.
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAgentDirTakesASymlinkToADirectory(t *testing.T) {
	client, home := inProcessSFTP(t)
	os.Mkdir(filepath.Join(home, "elsewhere"), 0o700)
	os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, AgentDir))
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAgentDirRefusesWhatItCannotUse(t *testing.T) {
	client, home := inProcessSFTP(t)
	if _, err := PrepareAgentDir(client, home, "../evil.sock", "", ""); err == nil {
		t.Fatal("a name with a slash was taken")
	}
	os.WriteFile(filepath.Join(home, AgentDir), []byte("a file"), 0o600)
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", "", ""); err == nil || !strings.Contains(err.Error(), ".nearhost") {
		t.Fatalf("a file in the way: %v", err)
	}
}

func TestPrepareAgentDirLeavesThisMachinesOwnSocketAlone(t *testing.T) {
	client, home := inProcessSFTP(t)
	own := filepath.Join(home, AgentDir, "k3f9q2.sock")
	os.MkdirAll(filepath.Dir(own), 0o700)
	os.WriteFile(own, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, ".probe-1"), nil, 0o600) // the server sees it: it is this machine
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", own, ".probe-1"); !errors.Is(err, ErrThisMachine) {
		t.Fatalf("got %v, want ErrThisMachine", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("this machine's socket was removed: %v", err)
	}
}

// A server whose socket path is this machine's own, that cannot be told from
// this machine, keeps the socket: without a probe, or when it cannot say
// whether it has the probe, it might be this machine.
func TestPrepareAgentDirKeepsTheSocketWhenItCannotTell(t *testing.T) {
	client, home := inProcessSFTP(t)
	own := filepath.Join(home, AgentDir, "k3f9q2.sock")
	os.MkdirAll(filepath.Dir(own), 0o700)
	os.WriteFile(own, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, "plain"), nil, 0o600)
	for name, probe := range map[string]string{
		"no probe":        "",
		"stat unanswered": "plain/.probe-1", // a file where a directory would be: neither found nor not found
	} {
		_, err := PrepareAgentDir(client, home, "k3f9q2.sock", own, probe)
		if err == nil || errors.Is(err, ErrThisMachine) || !strings.Contains(err.Error(), "cannot tell whether the server is this machine") {
			t.Errorf("%s: got %v", name, err)
		}
		if _, err := os.Stat(own); err != nil {
			t.Fatalf("%s: this machine's socket was removed: %v", name, err)
		}
	}
}

// Another machine can have its home at the path of this one's, as two Macs
// with one user name do. It does not see the probe made here, so it is
// readied as any server, and its old socket of this install removed.
func TestPrepareAgentDirReadiesAnotherMachineWithTheSameHomePath(t *testing.T) {
	client, home := inProcessSFTP(t)
	sock := filepath.Join(home, AgentDir, "k3f9q2.sock")
	os.MkdirAll(filepath.Dir(sock), 0o700)
	os.WriteFile(sock, nil, 0o600) // left by an earlier forward
	got, err := PrepareAgentDir(client, home, "k3f9q2.sock", sock, ".probe-1")
	if err != nil || got != sock {
		t.Fatalf("%q %v, want %q", got, err, sock)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("the old socket is still there: %v", err)
	}
}

func TestAServerThatIsThisMachineIsNotForwardedTo(t *testing.T) {
	s, log := newFakeSession(t)
	withAgent(s, ErrThisMachine)
	s.Start()
	waitFor(t, 3*time.Second, func() bool { return strings.Contains(readLog(t, log), "-O check") && s.Status() == StatusConnected }, "connected")
	time.Sleep(100 * time.Millisecond)
	if h := s.Health(); h.AgentErr != "" || h.AgentPath != "" {
		t.Fatalf("health %+v", h)
	}
	if strings.Contains(readLog(t, log), " -R ") {
		t.Fatalf("forwarded over this machine's own socket:\n%s", readLog(t, log))
	}
}

func TestAgentProblemsSayWhatKindTheyAre(t *testing.T) {
	long := "/home/" + strings.Repeat("x", 90) + "/.nearhost/k3f9q2.sock"
	for name, tc := range map[string]struct {
		remote, refusal string
		prepare         error
		want            AgentProblem
		says            string
	}{
		"refused":   {agentRemote, "remote port forwarding failed for listen path " + agentRemote, nil, AgentRefused, "refused to forward"},
		"too long":  {long, "remote port forwarding failed for listen path " + long, nil, AgentTooLong, "bytes, longer than a Unix socket path"},
		"not ready": {agentRemote, "", errors.New("make /home/dev/.nearhost: permission denied"), AgentNotReady, "permission denied"},
	} {
		t.Run(name, func(t *testing.T) {
			var env []string
			if tc.refusal != "" {
				env = append(env, "FAKE_SSH_RFORWARD_FAIL="+tc.refusal)
			}
			s, _ := newFakeSession(t, env...)
			withAgentAt(s, tc.remote, tc.prepare)
			s.Start()
			waitFor(t, 3*time.Second, func() bool { return s.Health().AgentErr != "" }, "the problem")
			h := s.Health()
			if h.AgentProblem != tc.want || !strings.Contains(h.AgentErr, tc.says) {
				t.Fatalf("health %+v", h)
			}
		})
	}
}
