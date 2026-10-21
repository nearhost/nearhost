package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

func fakeClient(t *testing.T, env ...string) (sshClient, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	return sshClient{path: testutil.FakeSSH(t), env: append([]string{"FAKE_SSH_LOG=" + log}, env...)}, log
}

func sshLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// noAddLeftovers checks that an `add` over ssh left no master process and no
// temporary run directory behind.
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
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "-N -M ") || !strings.HasSuffix(lines[0], " -- mini.lan") {
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

// sftpServer finds the OpenSSH SFTP server binary, which speaks the protocol
// on its standard streams and so can stand in for `ssh -s host sftp`.
func sftpServer(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/libexec/sftp-server", "/usr/lib/openssh/sftp-server", "/usr/libexec/openssh/sftp-server", "/usr/lib/ssh/sftp-server"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no sftp-server binary on this machine")
	return ""
}

// TestAddRemoteOverSFTP runs the real remote path of `add`: a master
// connection (the fake ssh) and the SFTP subsystem on it (a local sftp-server
// reading the fixture directory). The account is one that may use SFTP only:
// asked to run a command, the server refuses.
func TestAddRemoteOverSFTP(t *testing.T) {
	server := sftpServer(t)
	wrapper := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ \"$last\" = true ]; then printf '%s\\n' \"$*\" >> \"$FAKE_SSH_LOG\"; echo 'This service allows sftp connections only.' >&2; exit 1; fi\n" +
		"if [ \"$last\" = sftp ]; then printf '%s\\n' \"$*\" >> \"$FAKE_SSH_LOG\"; exec '" + server + "' -R; fi\n" +
		"exec '" + testutil.FakeSSH(t) + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "ssh.log")
	c := sshClient{path: wrapper, env: []string{"FAKE_SSH_LOG=" + log}}

	cfgPath, _ := existing(t)
	dir := fixture(t, "compose-literal")
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
			strings.Contains(line, " -O "), strings.HasPrefix(line, "-N -M ") && strings.HasSuffix(line, " -- mini.lan"):
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

// A Unix socket path holds 104 bytes on macOS (108 on Linux), and OpenSSH
// binds a master at its control path plus a 17-byte temporary suffix. The
// run directory of `add` must leave room whatever the server is called and
// wherever the system keeps temporary files.
// `add` does not handle signals: Ctrl-C ends it at once. Its master and SFTP
// process therefore stay in its process group and end with it.
func TestPrivateSessionStaysInTheForegroundProcessGroup(t *testing.T) {
	sess, end, err := sshClient{}.privateSession(config.Server{Name: "mini", SSHHost: "host.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	if !sess.Foreground {
		t.Fatal("the session of a remote add would outlive an interrupted add")
	}
	if up := (sshClient{}).session(config.Server{Name: "mini", SSHHost: "host.example"}, "/tmp/nh-run"); up.Foreground {
		t.Fatal("only the private session of add is a foreground one")
	}
}

func TestPrivateSessionSocketPathIsShort(t *testing.T) {
	const sunPath, sshSuffix, margin = 104, 17, 20
	for _, name := range []string{"mini", strings.Repeat("build-server-", 6), "deploy@build host"} {
		sess, end, err := sshClient{}.privateSession(config.Server{Name: name, SSHHost: "host.example"})
		if err != nil {
			t.Fatal(err)
		}
		sock := sess.SocketPath()
		dir := filepath.Dir(sock)
		if n := len(sock) + sshSuffix; n > sunPath-margin {
			t.Errorf("server %q: ssh would bind %d bytes (%s plus its suffix); the limit is %d", name, n, sock, sunPath)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("server %q: run directory %s is not a private directory: %v %v", name, dir, info, err)
		}
		if err := end(); err != nil {
			t.Errorf("server %q: %v", name, err)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("server %q: run directory %s was left behind", name, dir)
		}
	}
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
	server := sftpServer(t)
	remoteHome := t.TempDir()
	project := filepath.Join(remoteHome, "blog")
	if err := os.CopyFS(project, os.DirFS(fixture(t, "compose-literal"))); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ \"$last\" = sftp ]; then exec '" + server + "' -R -d '" + remoteHome + "'; fi\n" +
		"exec '" + testutil.FakeSSH(t) + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "ssh.log")
	c := sshClient{path: wrapper, env: []string{"FAKE_SSH_LOG=" + log}}
	localHome := "/nonexistent/home/dev"
	t.Setenv("HOME", localHome)

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
			if real, _ := filepath.EvalSymlinks(project); !ok || p.Path != real {
				t.Fatalf("project = %+v %v, want path %s", p, ok, real)
			}
			wantContains(t, out, "Routes found for blog (mini:"+p.Path+"):")
			if note := "Read " + given + " as " + p.Path + " on mini"; (name == "expanded") != strings.Contains(out, note) {
				t.Errorf("note %q shown wrongly:\n%s", note, out)
			}
		})
	}
}
