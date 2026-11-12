package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestPrepareAgentDirMakesThePrivateDirectoryAndRemovesOnlyItsOwnSocket(t *testing.T) {
	client, home := inProcessSFTP(t)
	other := filepath.Join(home, AgentDir, "zzzzzz.sock")
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, "k3f9q2.sock"), nil, 0o600)

	sock, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", "")
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
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAgentDirTakesASymlinkToADirectory(t *testing.T) {
	client, home := inProcessSFTP(t)
	os.Mkdir(filepath.Join(home, "elsewhere"), 0o700)
	os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, AgentDir))
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAgentDirRefusesWhatItCannotUse(t *testing.T) {
	client, home := inProcessSFTP(t)
	if _, err := PrepareAgentDir(client, home, "", "../evil.sock", "", "", ""); err == nil {
		t.Fatal("a name with a slash was taken")
	}
	os.WriteFile(filepath.Join(home, AgentDir), []byte("a file"), 0o600)
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", ""); err == nil || !strings.Contains(err.Error(), ".nearhost") {
		t.Fatalf("a file in the way: %v", err)
	}
}

func TestPrepareAgentDirLeavesThisMachinesOwnSocketAlone(t *testing.T) {
	client, home := inProcessSFTP(t)
	own := filepath.Join(home, AgentDir, "k3f9q2.sock")
	os.MkdirAll(filepath.Dir(own), 0o700)
	os.WriteFile(own, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, ".probe-1"), nil, 0o600) // the server sees it: it is this machine
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", own, ".probe-1", ""); !errors.Is(err, ErrThisMachine) {
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
		_, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", own, probe, "")
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
	got, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", sock, ".probe-1", "")
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

// runtimeRoot makes a stand-in for /run/user holding a runtime directory
// for this user with mode, and returns the stand-in and that directory.
func runtimeRoot(t *testing.T, mode os.FileMode) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return root, dir
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
	if info, _ := os.Stat(filepath.Join(run, "nearhost")); info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory mode %v", info.Mode().Perm())
	}
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

// A link left in ~/.nearhost by a run that used the runtime directory is
// removed when the socket goes back to ~/.nearhost.
func TestPrepareAgentDirRemovesAnOldLinkWhenTheSocketStaysHome(t *testing.T) {
	client, home := inProcessSFTP(t)
	os.MkdirAll(filepath.Join(home, AgentDir), 0o700)
	link := filepath.Join(home, AgentDir, "k3f9q2.sock")
	os.Symlink("/run/user/1000/nearhost/k3f9q2.sock", link)
	if _, err := PrepareAgentDir(client, home, t.TempDir(), "k3f9q2.sock", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the old link is still there: %v", err)
	}
}

// tokenAt reads the token file beside the socket k3f9q2.sock in home's
// ~/.nearhost, and fails the test unless only its owner can read it.
func tokenAt(t *testing.T, home string) string {
	t.Helper()
	p := filepath.Join(home, AgentDir, "k3f9q2.token")
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("the token file's mode is %v", info.Mode())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A forward to a loopback listener, as on Windows, needs its token: it
// goes beside the socket, as the header curl sends.
func TestPrepareAgentDirWritesTheToken(t *testing.T) {
	client, home := inProcessSFTP(t)
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", "abc"); err != nil {
		t.Fatal(err)
	}
	if got := tokenAt(t, home); got != "X-Nearhost-Token: abc\n" {
		t.Fatalf("token file %q", got)
	}
	// With a runtime directory the socket moves, but the token stays in
	// ~/.nearhost, beside the link agents look for.
	client, home = inProcessSFTP(t)
	root, _ := runtimeRoot(t, 0o700)
	if _, err := PrepareAgentDir(client, home, root, "k3f9q2.sock", "", "", "abc"); err != nil {
		t.Fatal(err)
	}
	if got := tokenAt(t, home); got != "X-Nearhost-Token: abc\n" {
		t.Fatalf("token file with a runtime directory %q", got)
	}
}

func TestPrepareAgentDirReplacesTheToken(t *testing.T) {
	client, home := inProcessSFTP(t)
	for _, tok := range []string{"abc", "def"} {
		if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", tok); err != nil {
			t.Fatal(err)
		}
	}
	if got := tokenAt(t, home); got != "X-Nearhost-Token: def\n" {
		t.Fatalf("token file %q", got)
	}
	// Nothing is left behind of the file written to replace it.
	entries, _ := os.ReadDir(filepath.Join(home, AgentDir))
	for _, e := range entries {
		if e.Name() != "k3f9q2.token" {
			t.Fatalf("left in ~/.nearhost: %s", e.Name())
		}
	}
}

func TestPrepareAgentDirRemovesATokenWhenNoneIsGiven(t *testing.T) {
	client, home := inProcessSFTP(t)
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", "abc"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, AgentDir, "zzzzzz.token")
	os.WriteFile(other, []byte("X-Nearhost-Token: zzz\n"), 0o600)
	if _, err := PrepareAgentDir(client, home, "", "k3f9q2.sock", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, AgentDir, "k3f9q2.token")); !os.IsNotExist(err) {
		t.Fatalf("the token file is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another install's token went: %v", err)
	}
}
