package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

func fakeClient(t *testing.T, env ...string) (sshClient, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	return sshClient{path: testutil.FakeSSH(t), env: append([]string{"FAKE_SSH_LOG=" + log}, env...)}, log
}

// serverPath is the local folder dir as a server path: a server's paths are
// POSIX, so on Windows, where this machine's sftp-server serves its own
// folders, C:\x is /C:/x, as OpenSSH's sftp-server for Windows names it.
// Elsewhere it is dir.
func serverPath(dir string) string {
	if runtime.GOOS == "windows" {
		return "/" + filepath.ToSlash(dir)
	}
	return dir
}

// serverDir is the local folder dir as this machine's sftp-server reports it
// when it resolves a path there: the real path (under /private on macOS),
// as a server path. sftp-server for Windows keeps the folder as it was given
// (a short 8.3 name stays short), so on Windows it is serverPath(dir).
func serverDir(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return serverPath(dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func sshLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// isLinkLaunch reports whether line, from the fake ssh's log, starts the
// connection to host that a session keeps up: the ControlMaster's
// `-N -M ... -- host`, or the per-process transport's
// `... -o ControlMaster=no ... -N -- host`, which is all Windows has. A line
// with a forward (-L or -R) or a control command (-O) is not one. Only the
// shape of the transport this run uses counts: TestMain unsets
// NEARHOST_TRANSPORT, so off Windows that is the ControlMaster, the same
// choice transport.New makes.
func isLinkLaunch(line, host string) bool {
	fields := strings.Fields(line)
	for _, f := range []string{"-O", "-L", "-R"} {
		if slices.Contains(fields, f) {
			return false
		}
	}
	link := slices.Contains(fields, "-M")
	if runtime.GOOS == "windows" || os.Getenv("NEARHOST_TRANSPORT") == "per-process" {
		link = slices.Contains(fields, "ControlMaster=no")
	}
	return link && slices.Contains(fields, "-N") && strings.HasSuffix(line, " -- "+host)
}

// noAddLeftovers checks that an `add` over ssh left no master process and no
// temporary run directory behind. It finds the run directory from the
// master's -S; the per-process transport (Windows) passes none, and that its
// run directory goes with the session is
// TestPrivateSessionRunDirIsUnderTempDirOnWindows.
func noAddLeftovers(t *testing.T, log string) {
	t.Helper()
	for _, line := range strings.Split(sshLog(t, log), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != "-S" || i+1 >= len(fields) {
				continue
			}
			dir := filepath.Dir(fields[i+1])
			if _, err := os.Stat(dir); err == nil {
				t.Errorf("run directory %s was left behind", dir)
			}
			if _, err := exec.LookPath("pgrep"); err == nil {
				testutil.WaitUntil(t, 5*time.Second, func() bool {
					out, _ := exec.Command("pgrep", "-f", dir).Output()
					return strings.TrimSpace(string(out)) == ""
				}, "ssh processes to exit")
			}
			return
		}
	}
}

// checkArgv is how the connectivity check runs ssh for the host mini.lan.
const checkArgv = "-o BatchMode=yes -o ConnectTimeout=10 -o RemoteCommand=none -o ClearAllForwardings=yes -o ControlMaster=no -o ControlPath=none -- mini.lan true"

func TestCheckServerRunsTheBatchModeCheck(t *testing.T) {
	c, log := fakeClient(t)
	if err := c.check(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sshLog(t, log)); got != checkArgv {
		t.Fatalf("ssh was run as %q", got)
	}

	c, _ = fakeClient(t, "FAKE_SSH_HOSTKEY_FAIL=1")
	err := c.check(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"})
	var ce *transport.CheckError
	if !errors.As(err, &ce) || ce.Stderr != "Host key verification failed." {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRemoteReportsRefusedAccessWithTheHint(t *testing.T) {
	c, log := fakeClient(t, "FAKE_SSH_AUTH_FAIL=1")
	_, _, _, err := c.openRemote(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}, "/srv/app")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"user@host: Permission denied (publickey).", "run `ssh mini.lan` once in a terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// One connection attempt and nothing else: no command was run on the
	// server to test it, and a server that refuses is not tried again.
	lines := strings.Split(strings.TrimSpace(sshLog(t, log)), "\n")
	if len(lines) != 1 || !isLinkLaunch(lines[0], "mini.lan") {
		t.Fatalf("ssh was run as:\n%s", strings.Join(lines, "\n"))
	}
	noAddLeftovers(t, log)
}

func TestOpenRemoteStopsTheMasterWhenSFTPIsUnavailable(t *testing.T) {
	c, log := fakeClient(t) // the fake ssh has no sftp subsystem
	_, _, _, err := c.openRemote(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}, "/srv/app")
	if err == nil || !strings.Contains(err.Error(), "sftp") {
		t.Fatalf("expected an sftp error, got %v", err)
	}
	noAddLeftovers(t, log)
}

// TestAddRemoteOverSFTP runs the real remote path of `add`: a master
// connection (the fake ssh) and the SFTP subsystem on it (a local sftp-server
// reading the fixture directory). The account is one that may use SFTP only:
// asked to run a command, the server refuses.
func TestAddRemoteOverSFTP(t *testing.T) {
	log := filepath.Join(t.TempDir(), "ssh.log")
	c := sshClient{path: testutil.FakeSSH(t), env: []string{
		"FAKE_SSH_LOG=" + log,
		"FAKE_SSH_SFTP_SERVER=" + testutil.SFTPServer(t),
		"FAKE_SSH_CHECK_REFUSE=This service allows sftp connections only.",
	}}

	cfgPath, _ := existing(t)
	dir := serverPath(fixture(t, "compose-literal"))
	out, err := execute(t, newAddCmd(addBackend{openRemote: c.openRemote}), "--config", cfgPath, "add", dir, "--server", "mini", "--name", "blog", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, ok := loadSaved(t, cfgPath).Project("blog")
	if !ok || p.Server != "mini" || p.Path != dir {
		t.Fatalf("project = %+v %v", p, ok)
	}
	if web, _ := p.WebRoute(); web.Port != 8080 {
		t.Fatalf("routes = %+v", p.Routes)
	}
	// Nothing but the master, its control commands and the SFTP subsystem
	// ran: no command on the server, not even to test the connection.
	for _, line := range strings.Split(strings.TrimSpace(sshLog(t, log)), "\n") {
		switch {
		case strings.HasSuffix(line, " -s -- mini.lan sftp"),
			strings.Contains(line, " -O "), isLinkLaunch(line, "mini.lan"):
		default:
			t.Errorf("unexpected ssh invocation: %s", line)
		}
	}
	noAddLeftovers(t, log)

	// A path that is not on the server is reported as such.
	_, err = execute(t, newAddCmd(addBackend{openRemote: c.openRemote}), "--config", cfgPath, "add", "/no/such/dir", "--server", "mini")
	if err == nil || !strings.Contains(err.Error(), "/no/such/dir") || !strings.Contains(err.Error(), "mini") {
		t.Fatalf("expected a missing-path error, got %v", err)
	}
}

// `add` does not handle signals: Ctrl-C ends it at once. Its master and SFTP
// process therefore stay in its process group and end with it. Both
// transports have the setting (Windows has process groups too, and puts a
// background ssh in a new one), so this reads it from whichever transport
// the OS gets.
func TestPrivateSessionStaysInTheForegroundProcessGroup(t *testing.T) {
	sess, _, end, err := sshClient{}.privateSession(config.Server{Name: "mini", SSHHost: "host.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	if !foreground(t, sess) {
		t.Fatal("the session of a remote add would outlive an interrupted add")
	}
	if foreground(t, sshClient{}.session(config.Server{Name: "mini", SSHHost: "host.example"}, "/tmp/nh-run")) {
		t.Fatal("only the private session of add is a foreground one")
	}
}

// foreground reads the Foreground setting of tr. Both transports have the
// field, but the per-process one is not exported from its package.
func foreground(t *testing.T, tr transport.Transport) bool {
	t.Helper()
	v := reflect.ValueOf(tr)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		t.Fatalf("%T is not a struct, so it has no Foreground setting", tr)
	}
	f := v.FieldByName("Foreground")
	if f.Kind() != reflect.Bool {
		t.Fatalf("%T has no Foreground setting", tr)
	}
	return f.Bool()
}

func TestProjectPathOn(t *testing.T) {
	dirs := map[string]bool{"/home/dev": true, "/home/dev/code/blog": true, "/Users/dev/code/shop": true}
	isDir := func(p string) bool { return dirs[p] }
	home := func() (string, error) { return "/home/dev", nil }
	for _, c := range []struct{ given, localHome, want string }{
		{"~", "/Users/dev", "/home/dev"},
		{"~/code/blog", "/Users/dev", "/home/dev/code/blog"},
		// The shell expanded ~ to this machine's home before add saw it.
		{"/Users/dev/code/blog", "/Users/dev", "/home/dev/code/blog"},
		// A path under this machine's home that is there on the server too
		// is taken as it is.
		{"/Users/dev/code/shop", "/Users/dev", "/Users/dev/code/shop"},
		// Neither is there: the path is reported as given.
		{"/Users/dev/code/none", "/Users/dev", "/Users/dev/code/none"},
		{"/srv/www", "/Users/dev", "/srv/www"},
		// A home with no slash after it is another directory.
		{"/Users/devops/code/blog", "/Users/dev", "/Users/devops/code/blog"},
		{"/Users/dev/code/blog", "", "/Users/dev/code/blog"},
	} {
		got, err := projectPathOn(c.given, c.localHome, home, isDir)
		if err != nil || got != c.want {
			t.Errorf("projectPathOn(%q, %q) = %q, %v; want %q", c.given, c.localHome, got, err, c.want)
		}
	}
	if _, err := projectPathOn("~/code", "/Users/dev", func() (string, error) { return "", errors.New("no realpath") }, isDir); err == nil || !strings.Contains(err.Error(), "no realpath") {
		t.Errorf("a home that cannot be found gave %v", err)
	}
}

// TestAddRemoteResolvesPathsUnderTheServerHome reads ~/blog, and the same
// path as the shell expands it on this machine, from the home directory of
// the account on the server.
func TestAddRemoteResolvesPathsUnderTheServerHome(t *testing.T) {
	server := testutil.SFTPServer(t)
	remoteHome := t.TempDir()
	project := filepath.Join(remoteHome, "blog")
	if err := os.CopyFS(project, os.DirFS(fixture(t, "compose-literal"))); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "ssh.log")
	c := sshClient{path: testutil.FakeSSH(t), env: []string{
		"FAKE_SSH_LOG=" + log,
		"FAKE_SSH_SFTP_SERVER=" + server,
		"FAKE_SSH_SFTP_HOME=" + remoteHome,
	}}
	localHome := "/nonexistent/home/dev"
	testutil.SetHome(t, localHome)

	for name, given := range map[string]string{"tilde": "~/blog", "expanded": localHome + "/blog"} {
		t.Run(name, func(t *testing.T) {
			cfgPath, _ := existing(t)
			out, err := execute(t, newAddCmd(addBackend{openRemote: c.openRemote}), "--config", cfgPath, "add", given, "--server", "mini", "--web", "8080")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			// sftp-server reports the real path, which on macOS is under
			// /private.
			p, ok := loadSaved(t, cfgPath).Project("blog")
			if real := serverDir(t, project); !ok || p.Path != real {
				t.Fatalf("project = %+v %v, want path %s", p, ok, real)
			}
			wantContains(t, out, "Routes found for blog (mini:"+p.Path+"):")
			if note := "Read " + given + " as " + p.Path + " on mini"; (name == "expanded") != strings.Contains(out, note) {
				t.Errorf("note %q shown wrongly:\n%s", note, out)
			}
		})
	}
}

// With the socket in the server's runtime directory, doctor's check leaves
// a link to it in ~/.nearhost: both go, and no other install's socket.
func TestDoctorRemovesItsSocketAndItsLink(t *testing.T) {
	home := t.TempDir()
	const name = "0abcde.sock"
	run := filepath.Join(home, "run", "nearhost")
	agents := filepath.Join(home, transport.AgentDir)
	for _, dir := range []string{run, agents} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(run, name)
	other := filepath.Join(agents, "k3f9q2.sock")
	for _, f := range []string{socket, other} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Symlink(t, socket, filepath.Join(agents, name))

	// An SFTP server in this process, its working directory the home.
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{serverReader, serverWriter}, sftp.WithServerWorkingDirectory(serverPath(home)))
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	client, err := sftp.NewClientPipe(clientReader, clientWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Server first: client.Close waits on a reader only the server close unblocks.
		server.Close()
		client.Close()
	}()

	// The server's paths are POSIX: on Windows C:\x is /C:/x to it.
	removeDoctorSocket(client, serverPath(socket), name)

	for _, gone := range []string{socket, filepath.Join(agents, name)} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", gone, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another install's socket went: %v", err)
	}
}
