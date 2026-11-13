//go:build unix

// Two kinds of test here run on Unix only. Most drive a Session, the
// ControlMaster transport Windows does not have, which forwards the agent
// socket with ssh -O forward; perprocess_test.go has the per-process ones.
// The two PrepareAgentDir tests here need a private runtime directory,
// /run/user/<uid>: one named for this user's uid, owned by the account that
// owns home, with a mode only it can open. Windows has none of those: the
// uid is -1, the in-process SFTP server reports no owner, and a folder's
// mode there is 0777 whatever chmod asked for. The runtime directory would
// never be used, and the tests would pass or fail for that reason alone.

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

	"github.com/nearhost/nearhost/internal/testutil"
)

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
	if !strings.Contains(readLog(t, log), "-O forward -R "+agentRemote+":/run/nh/agent-mini.sock -- mini") {
		t.Fatalf("no -R forward in:\n%s", readLog(t, log))
	}
	s.Stop()
	if !strings.Contains(readLog(t, log), "-O cancel -R "+agentRemote+":/run/nh/agent-mini.sock -- mini") {
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

// Where the server has a private runtime directory, the socket goes there,
// as SELinux on Fedora keeps sshd from making sockets in home folders, and
// ~/.nearhost holds a link to it, where agents look.
func TestPrepareAgentDirUsesTheRuntimeDirectory(t *testing.T) {
	client, home := inProcessSFTP(t)
	root, run := runtimeRoot(t, 0o700)
	// Left by earlier runs: a socket in ~/.nearhost and one in the runtime
	// directory.
	os.MkdirAll(filepath.Join(home, AgentDir), 0o700)
	os.WriteFile(filepath.Join(home, AgentDir, "k3f9q2.sock"), nil, 0o600)
	os.MkdirAll(filepath.Join(run, "nearhost"), 0o755)
	os.WriteFile(filepath.Join(run, "nearhost", "k3f9q2.sock"), nil, 0o600)

	sock, err := PrepareAgentDir(client, home, root, "k3f9q2.sock", "", "", "")
	want := filepath.Join(run, "nearhost", "k3f9q2.sock")
	if err != nil || sock != want {
		t.Fatalf("%q %v, want %q", sock, err, want)
	}
	if _, err := os.Lstat(want); !os.IsNotExist(err) {
		t.Fatalf("the old socket in the runtime directory is still there: %v", err)
	}
	testutil.WantPerm(t, filepath.Join(run, "nearhost"), 0o700)
	if to, err := os.Readlink(filepath.Join(home, AgentDir, "k3f9q2.sock")); err != nil || to != want {
		t.Fatalf("~/.nearhost/k3f9q2.sock links to %q (%v), want %q", to, err, want)
	}
	// Again, with the link and nothing else there.
	if got, err := PrepareAgentDir(client, home, root, "k3f9q2.sock", "", "", ""); err != nil || got != want {
		t.Fatalf("again: %q %v", got, err)
	}
}

// A runtime directory that is missing, or that others can open, is not
// used: the socket stays in ~/.nearhost, as before.
func TestPrepareAgentDirKeepsTheHomeSocketWithoutAPrivateRuntimeDirectory(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"missing": 0, "open to others": 0o755} {
		t.Run(name, func(t *testing.T) {
			client, home := inProcessSFTP(t)
			root := t.TempDir()
			if mode != 0 {
				root, _ = runtimeRoot(t, mode)
			}
			sock, err := PrepareAgentDir(client, home, root, "k3f9q2.sock", "", "", "")
			if err != nil || sock != filepath.Join(home, AgentDir, "k3f9q2.sock") {
				t.Fatalf("%q %v", sock, err)
			}
		})
	}
}
