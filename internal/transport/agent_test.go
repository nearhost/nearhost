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
	var n atomic.Int32
	s.prepareAgent = func(ctx context.Context, f AgentForward) (string, error) {
		n.Add(1)
		if f.Name != "k3f9q2.sock" {
			return "", errors.New("wrong name " + f.Name)
		}
		return agentRemote, err
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

	sock, err := PrepareAgentDir(client, home, "k3f9q2.sock", "")
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
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAgentDirTakesASymlinkToADirectory(t *testing.T) {
	client, home := inProcessSFTP(t)
	os.Mkdir(filepath.Join(home, "elsewhere"), 0o700)
	os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, AgentDir))
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAgentDirRefusesWhatItCannotUse(t *testing.T) {
	client, home := inProcessSFTP(t)
	if _, err := PrepareAgentDir(client, home, "../evil.sock", ""); err == nil {
		t.Fatal("a name with a slash was taken")
	}
	os.WriteFile(filepath.Join(home, AgentDir), []byte("a file"), 0o600)
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", ""); err == nil || !strings.Contains(err.Error(), ".nearhost") {
		t.Fatalf("a file in the way: %v", err)
	}
}

func TestPrepareAgentDirLeavesThisMachinesOwnSocketAlone(t *testing.T) {
	client, home := inProcessSFTP(t)
	own := filepath.Join(home, AgentDir, "k3f9q2.sock")
	os.MkdirAll(filepath.Dir(own), 0o700)
	os.WriteFile(own, nil, 0o600)
	if _, err := PrepareAgentDir(client, home, "k3f9q2.sock", own); !errors.Is(err, ErrThisMachine) {
		t.Fatalf("got %v, want ErrThisMachine", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("this machine's socket was removed: %v", err)
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
