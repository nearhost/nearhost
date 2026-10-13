package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// fakeMachine is a machine on which every doctor check passes; tests break
// one thing at a time.
type fakeMachine struct {
	trust     *fakeTrust
	portErr   map[string]error // "tcp 443" -> what binding it returns
	holders   map[int]string
	hosts     string
	hostsErr  error
	serverErr map[string]error
	goos      string
	lowLevel  level
	lowDetail string

	probed      []string
	hostsReads  int
	serverCalls []string
	lowAsked    []int
}

func healthy() *fakeMachine {
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSCovered}
	return &fakeMachine{
		trust:    f,
		hosts:    "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 api.test\n127.0.0.1 web.test\n# nearhost:end\n",
		goos:     "darwin",
		lowLevel: levelOK, lowDetail: "has cap_net_bind_service",
	}
}

func (f *fakeMachine) backend() doctorBackend {
	return doctorBackend{
		trust: f.trust.backend(),
		probePort: func(network string, port int) error {
			key := fmt.Sprintf("%s %d", network, port)
			f.probed = append(f.probed, key)
			return f.portErr[key]
		},
		holderOf:  func(_ context.Context, port int) string { return f.holders[port] },
		readHosts: func() (string, error) { f.hostsReads++; return f.hosts, f.hostsErr },
		checkServer: func(_ context.Context, srv config.Server) error {
			f.serverCalls = append(f.serverCalls, srv.SSHHost)
			return f.serverErr[srv.Name]
		},
		goos: f.goos,
		lowPorts: func(port int) (level, string) {
			f.lowAsked = append(f.lowAsked, port)
			return f.lowLevel, f.lowDetail
		},
	}
}

// doctorConfig is two projects, one of them on the server "mini".
func doctorConfig(t *testing.T) string {
	t.Helper()
	path := configHome(t)
	saveConfig(t, path, &config.Config{Servers: mini, Projects: []config.Project{
		localProject("web", 8000, true), remoteProject("api"),
	}})
	return path
}

func TestDoctorReportsEveryCheckPassing(t *testing.T) {
	path := doctorConfig(t)
	f := healthy()
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	want := "" +
		"ok    config           " + path + "\n" +
		"ok    port 443         free\n" +
		"ok    port 80          free\n" +
		"ok    local CA         trusted by the system trust store\n" +
		"ok    browser stores   the NSS stores trust the CA\n" +
		"ok    hosts block      2 aliases\n" +
		"ok    server mini      ssh mini.lan works without a prompt\n" +
		"\n" +
		"All checks passed.\n"
	if out != want {
		t.Fatalf("doctor printed:\n%s\nwant:\n%s", out, want)
	}
	if strings.Join(f.probed, "; ") != "tcp 443; udp 443; tcp 80" {
		t.Errorf("ports probed: %v", f.probed)
	}
	if want := filepath.Join(filepath.Dir(path), "caddy"); len(f.trust.rootDirs) != 1 || f.trust.rootDirs[0] != want {
		t.Errorf("CA looked up in %v, want %s", f.trust.rootDirs, want)
	}
	if f.trust.installs+f.trust.nssInstalls+f.trust.uninstall+len(f.trust.ensured) != 0 {
		t.Error("doctor changed the trust stores or created the CA")
	}
	if len(f.lowAsked) != 0 {
		t.Error("the Linux privileged-port check ran on another system")
	}
}

func TestDoctorFailsOnATakenPortAndNamesTheHolder(t *testing.T) {
	doctorConfig(t)
	f := healthy()
	f.portErr = map[string]error{"tcp 8443": syscall.EADDRINUSE, "udp 8443": syscall.EADDRINUSE, "tcp 8080": syscall.EADDRINUSE}
	f.holders = map[int]string{8443: "nginx (pid 12)"}
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor", "--https-port", "8443", "--http-port", "8080")
	if err == nil || err.Error() != "2 checks failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out,
		"FAIL  port 8443        in use by nginx (pid 12); UDP (for HTTP/3): address already in use\n"+
			"      fix: stop that process (expected while `nearhost up` is running), or give `nearhost up` another --https-port\n",
		"FAIL  port 8080        address already in use\n"+
			"      fix: stop the process using it (expected while `nearhost up` is running), or give `nearhost up` another --http-port\n",
		"\n2 checks failed.\n")

	// Taken on UDP alone: the report says so instead of blaming TCP.
	f = healthy()
	f.portErr = map[string]error{"udp 8443": syscall.EADDRINUSE}
	f.holders = map[int]string{8443: "a TCP listener nobody asked about"}
	out, err = execute(t, newDoctorCmd(f.backend()), "doctor", "--https-port", "8443", "--http-port", "8080")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out, "FAIL  port 8443        UDP (for HTTP/3): address already in use\n"+
		"      fix: stop the process using it (expected while `nearhost up` is running), or give `nearhost up` another --https-port\n",
		"ok    port 8080        free\n")
}

func TestDoctorChecksPrivilegedPortsOnLinuxOnly(t *testing.T) {
	doctorConfig(t)
	f := healthy()
	f.goos = "linux"
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	wantContains(t, out, "ok    privileged ports has cap_net_bind_service\n")
	if len(f.lowAsked) != 1 || f.lowAsked[0] != 80 {
		t.Errorf("asked about ports %v, want the lower one", f.lowAsked)
	}

	f = healthy()
	f.goos = "linux"
	f.lowLevel, f.lowDetail = levelFail, "not root and the binary lacks cap_net_bind_service"
	f.portErr = map[string]error{"tcp 443": syscall.EACCES, "udp 443": syscall.EACCES, "tcp 80": syscall.EACCES}
	out, err = execute(t, newDoctorCmd(f.backend()), "doctor")
	if err == nil || err.Error() != "3 checks failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out,
		"FAIL  port 443         permission denied\n      fix: sudo setcap cap_net_bind_service=+ep \"$(command -v nearhost)\"\n",
		"FAIL  privileged ports not root and the binary lacks cap_net_bind_service\n      fix: sudo setcap cap_net_bind_service=+ep \"$(command -v nearhost)\"\n")

	// Ports above 1023 need no privilege, so there is nothing to check.
	f = healthy()
	f.goos = "linux"
	out, err = execute(t, newDoctorCmd(f.backend()), "doctor", "--https-port", "8443", "--http-port", "8080")
	if err != nil || strings.Contains(out, "privileged") || len(f.lowAsked) != 0 {
		t.Fatalf("unexpected privileged-port check: %v\n%s", err, out)
	}
}

func TestDoctorReportsTheCAAndTheBrowserStoresSeparately(t *testing.T) {
	const trustFix = "      fix: run `nearhost trust`\n"
	cases := []struct {
		name    string
		mutate  func(*fakeTrust)
		want    []string
		absent  string
		failure string
	}{
		{"no CA yet", func(f *fakeTrust) { f.rootErr = fmt.Errorf("open root.crt: %w", fs.ErrNotExist) },
			[]string{"FAIL  local CA         not created yet\n" + trustFix}, "browser stores", "1 check failed"},
		{"unreadable CA", func(f *fakeTrust) { f.rootErr = errors.New("root.crt is not PEM") },
			[]string{"FAIL  local CA         root.crt is not PEM\n"}, "browser stores", "1 check failed"},
		{"CA not trusted", func(f *fakeTrust) { f.trusted = false },
			[]string{"FAIL  local CA         exists, but the system trust store does not trust it\n" + trustFix,
				"ok    browser stores   the NSS stores trust the CA\n"}, "", "1 check failed"},
		{"browser stores lack it", func(f *fakeTrust) { f.nss = proxy.NSSStatus{State: proxy.NSSMissing} },
			[]string{"ok    local CA         trusted by the system trust store\n",
				"warn  browser stores   the NSS stores (Firefox, and on Linux the shared database Chromium-family browsers use) do not trust the CA\n" + trustFix,
				"\nNo failures, 1 warning.\n"}, "", ""},
		{"browser stores unknown", func(f *fakeTrust) {
			f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, Detail: `"certutil" not found (install it with "brew install nss")`}
		},
			[]string{"warn  browser stores   not checked: \"certutil\" not found (install it with \"brew install nss\")\n" +
				"      fix: only Firefox (and on Linux Chromium-family browsers) need this; set them up, then run `nearhost trust`\n"}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doctorConfig(t)
			f := healthy()
			tc.mutate(f.trust)
			out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
			if got := fmt.Sprint(err); (tc.failure == "" && err != nil) || (tc.failure != "" && got != tc.failure) {
				t.Errorf("err = %v, want %q", err, tc.failure)
			}
			wantContains(t, out, tc.want...)
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("output mentions %q:\n%s", tc.absent, out)
			}
		})
	}
}

func TestDoctorComparesTheHostsBlockWithTheConfiguredAliases(t *testing.T) {
	const fix = "      fix: run `nearhost up`; it updates the block and asks for your password\n"
	cases := []struct {
		name  string
		hosts string
		err   error
		want  string
	}{
		{"no block", "127.0.0.1 localhost\n", nil, "FAIL  hosts block      missing api.test, web.test\n" + fix},
		{"one missing, one stale", "# nearhost:start\n127.0.0.1 old.test\n127.0.0.1 web.test\n# nearhost:end\n", nil,
			"FAIL  hosts block      missing api.test; no longer configured: old.test\n" + fix},
		{"right aliases, block not as nearhost writes it", "# nearhost:start\n127.0.0.1 web.test\n127.0.0.1 api.test\n# nearhost:end\n", nil,
			"FAIL  hosts block      the block is not in the form nearhost writes\n" + fix},
		{"unreadable", "", errors.New("open /etc/hosts: permission denied"), "FAIL  hosts block      open /etc/hosts: permission denied\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doctorConfig(t)
			f := healthy()
			f.hosts, f.hostsErr = tc.hosts, tc.err
			out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
			if err == nil || err.Error() != "1 check failed" {
				t.Errorf("err = %v", err)
			}
			wantContains(t, out, tc.want)
		})
	}

	t.Run("--no-hosts skips the check", func(t *testing.T) {
		doctorConfig(t)
		f := healthy()
		f.hosts = ""
		out, err := execute(t, newDoctorCmd(f.backend()), "doctor", "--no-hosts")
		if err != nil || strings.Contains(out, "hosts block") || f.hostsReads != 0 {
			t.Fatalf("err = %v, hosts read %d times\n%s", err, f.hostsReads, out)
		}
	})

	t.Run("nothing configured needs no block", func(t *testing.T) {
		configHome(t)
		f := healthy()
		f.hosts = "127.0.0.1 localhost\n"
		out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
		if err != nil {
			t.Fatalf("doctor: %v\n%s", err, out)
		}
		wantContains(t, out, "ok    hosts block      no aliases configured\n")
	})
}

func TestDoctorShowsTheSSHErrorVerbatimWithTheHint(t *testing.T) {
	doctorConfig(t)
	f := healthy()
	f.serverErr = map[string]error{"mini": &transport.CheckError{Host: "mini.lan", Stderr: "Host key verification failed.\nssh: second line"}}
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out, ""+
		"FAIL  server mini      Host key verification failed.\n"+
		"                       ssh: second line\n"+
		"      fix: run `ssh mini.lan` once in a terminal to fix access, then run `nearhost doctor` again\n")
	if len(f.serverCalls) != 1 || f.serverCalls[0] != "mini.lan" {
		t.Errorf("servers checked: %v", f.serverCalls)
	}
}

func TestDoctorKeepsCheckingAfterAnInvalidConfig(t *testing.T) {
	path := configHome(t)
	saveConfig(t, path, &config.Config{Projects: []config.Project{{Name: "broken", Server: "gone", Alias: "broken.test"}}})
	f := healthy()
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor", "--no-hosts")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out,
		"FAIL  config           "+path+": project \"broken\" uses unknown server \"gone\"\n      fix: edit "+path+"\n",
		"ok    port 443         free\n", "ok    local CA ")
	if _, err := execute(t, newDoctorCmd(f.backend()), "doctor", "extra"); err == nil {
		t.Error("doctor must reject positional arguments")
	}
}
