package transport

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

const agentRemote = "/home/dev/.nearhost/k3f9q2.sock"

// PrepareAgentDir takes the server's paths, which are POSIX, and builds the
// socket's with path.Join. The in-process SFTP server serves this machine's
// temp folder, so a test hands it that folder with forward slashes, as
// filepath.ToSlash gives it, and expects paths built the same way. Windows
// opens C:/x/.nearhost as it does C:\x\.nearhost; on Unix nothing changes.

func TestPrepareAgentDirMakesThePrivateDirectoryAndRemovesOnlyItsOwnSocket(t *testing.T) {
	client, home := inProcessSFTP(t)
	other := filepath.Join(home, AgentDir, "zzzzzz.sock")
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, "k3f9q2.sock"), nil, 0o600)

	sock, err := PrepareAgentDir(client, filepath.ToSlash(home), "", "k3f9q2.sock", "", "", "")
	if err != nil || sock != path.Join(filepath.ToSlash(home), AgentDir, "k3f9q2.sock") {
		t.Fatalf("%q %v", sock, err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("its own socket is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another install's socket went: %v", err)
	}
	testutil.WantPerm(t, filepath.Join(home, AgentDir), 0o700)
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
	home = filepath.ToSlash(home)
	own := path.Join(home, AgentDir, "k3f9q2.sock")
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
	home = filepath.ToSlash(home)
	own := path.Join(home, AgentDir, "k3f9q2.sock")
	os.MkdirAll(filepath.Dir(own), 0o700)
	os.WriteFile(own, nil, 0o600)
	os.WriteFile(filepath.Join(home, AgentDir, "plain"), nil, 0o600)
	// A file where a directory would be: a POSIX server answers ENOTDIR,
	// neither found nor not found. Windows, which the in-process server runs
	// on there, says the path is not found (ERROR_PATH_NOT_FOUND), but fails
	// a name with a wildcard in it (ERROR_INVALID_NAME), which says neither.
	unanswered := "plain/.probe-1"
	if runtime.GOOS == "windows" {
		unanswered = ".probe-*"
	}
	for name, probe := range map[string]string{
		"no probe":        "",
		"stat unanswered": unanswered,
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
	home = filepath.ToSlash(home)
	sock := path.Join(home, AgentDir, "k3f9q2.sock")
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
	if !info.Mode().IsRegular() {
		t.Fatalf("the token file's mode is %v", info.Mode())
	}
	testutil.WantPerm(t, p, 0o600)
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
