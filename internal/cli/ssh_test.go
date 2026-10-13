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

func TestCheckServerRunsTheBatchModeCheck(t *testing.T) {
	c, log := fakeClient(t)
	if err := c.check(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sshLog(t, log)); got != "-o BatchMode=yes -o ConnectTimeout=10 mini.lan true" {
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
	_, _, err := c.openRemote(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}, "/srv/app")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"user@host: Permission denied (publickey).", "run `ssh mini.lan` once in a terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// Only the check ran: no master was started for a server that refuses us.
	if got := strings.TrimSpace(sshLog(t, log)); got != "-o BatchMode=yes -o ConnectTimeout=10 mini.lan true" {
		t.Fatalf("ssh was run as:\n%s", got)
	}
}

func TestOpenRemoteStopsTheMasterWhenSFTPIsUnavailable(t *testing.T) {
	c, log := fakeClient(t) // the fake ssh has no sftp subsystem
	_, _, err := c.openRemote(context.Background(), config.Server{Name: "mini", SSHHost: "mini.lan"}, "/srv/app")
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

// TestAddRemoteOverSFTP runs the real remote path of `add`: the batch-mode
// check, a master connection (the fake ssh) and the SFTP subsystem on it (a
// local sftp-server reading the fixture directory).
func TestAddRemoteOverSFTP(t *testing.T) {
	server := sftpServer(t)
	wrapper := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ \"$last\" = sftp ]; then printf '%s\\n' \"$*\" >> \"$FAKE_SSH_LOG\"; exec '" + server + "' -R; fi\n" +
		"exec '" + testutil.FakeSSH(t) + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "ssh.log")
	c := sshClient{path: wrapper, env: []string{"FAKE_SSH_LOG=" + log}}

	cfgPath, _ := existing(t)
	dir := fixture(t, "compose-literal")
	out, err := execute(t, newAddCmd(addBackend{openRemote: c.openRemote}), "--config", cfgPath, "add", dir, "--server", "mini", "--name", "blog")
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
	// Nothing but the check, the master, its control commands and the SFTP
	// subsystem ran: no remote shell command.
	for _, line := range strings.Split(strings.TrimSpace(sshLog(t, log)), "\n") {
		switch {
		case strings.HasSuffix(line, " mini.lan true"), strings.HasSuffix(line, " -s mini.lan sftp"),
			strings.Contains(line, " -O "), strings.HasPrefix(line, "-N -M "):
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
