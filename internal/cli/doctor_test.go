package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hosts"
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
	exe       string

	probed      []string
	hostsReads  int
	lookup      hosts.Lookup
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
		lookup:    f.lookup,
		holderOf:  func(_ context.Context, port int) string { return f.holders[port] },
		readHosts: func() (string, error) { f.hostsReads++; return f.hosts, f.hostsErr },
		checkServer: func(_ context.Context, srv config.Server) error {
			f.serverCalls = append(f.serverCalls, srv.SSHHost)
			return f.serverErr[srv.Name]
		},
		goos: f.goos,
		exe:  f.exe,
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

	// Installed from npm, `command -v nearhost` is the Node launcher, and a
	// capability on a script does nothing: the fix names the binary itself.
	f = healthy()
	f.goos = "linux"
	f.exe = "/home/me/.npm/_npx/1a2b/node_modules/@nearhost/cli-linux-x64/bin/nearhost"
	f.lowLevel, f.lowDetail = levelFail, "not root and the binary lacks cap_net_bind_service"
	out, _ = execute(t, newDoctorCmd(f.backend()), "doctor")
	wantContains(t, out, "      fix: sudo setcap cap_net_bind_service=+ep '/home/me/.npm/_npx/1a2b/node_modules/@nearhost/cli-linux-x64/bin/nearhost'\n")

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
	f := healthy() // its hosts file has a block, which nothing can be compared with
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	const unread = "not checked: the config could not be read\n      fix: correct the config (see above), then run `nearhost doctor` again\n"
	want := "" +
		"FAIL  config           " + path + ": project \"broken\" uses unknown server \"gone\"\n      fix: edit " + path + "\n" +
		"ok    port 443         free\n" +
		"ok    port 80          free\n" +
		"ok    local CA         trusted by the system trust store\n" +
		"ok    browser stores   the NSS stores trust the CA\n" +
		"warn  hosts block      " + unread +
		"warn  servers          " + unread +
		"\n" +
		"1 check failed.\n"
	if out != want {
		t.Fatalf("doctor printed:\n%s\nwant:\n%s", out, want)
	}
	if f.hostsReads != 0 || len(f.serverCalls) != 0 {
		t.Errorf("checked the hosts file %d times and servers %v without a config", f.hostsReads, f.serverCalls)
	}

	// --no-hosts still leaves the hosts block out altogether.
	out, err = execute(t, newDoctorCmd(f.backend()), "doctor", "--no-hosts")
	if err == nil || strings.Contains(out, "hosts block") || !strings.Contains(out, "warn  servers          not checked") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if _, err := execute(t, newDoctorCmd(f.backend()), "doctor", "extra"); err == nil {
		t.Error("doctor must reject positional arguments")
	}
}

// `nearhost up` does not start a project whose alias is not a valid .test
// name. doctor must say so rather than leave the alias out and report the
// hosts block as fine.
func TestDoctorFailsOnAnInvalidAlias(t *testing.T) {
	path := configHome(t)
	bad := localProject("legacy", 8001, true)
	bad.Alias = "Legacy.example"
	saveConfig(t, path, &config.Config{Projects: []config.Project{bad}})
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n"
	for _, args := range [][]string{{"doctor"}, {"doctor", "--no-hosts"}} {
		out, err := execute(t, newDoctorCmd(f.backend()), args...)
		if err == nil || err.Error() != "1 check failed" {
			t.Errorf("%v: err = %v\n%s", args, err, out)
		}
		wantContains(t, out, "FAIL  aliases          project \"legacy\": invalid host \"Legacy.example\": must be a lowercase host name, not localhost or a wildcard\n"+
			"      fix: correct the alias in "+path+"; `nearhost up` does not start a project with such an alias\n")
	}

	// The hosts block is still compared with the aliases that are valid.
	saveConfig(t, path, &config.Config{Projects: []config.Project{bad, localProject("web", 8000, true)}})
	out, err := execute(t, newDoctorCmd(f.backend()), "doctor")
	if err == nil || err.Error() != "2 checks failed" {
		t.Errorf("err = %v\n%s", err, out)
	}
	wantContains(t, out, "FAIL  aliases          project \"legacy\": invalid host", "FAIL  hosts block      missing web.test\n")
}

// doctor says when the local CA expires.
func TestDoctorSaysWhenTheCAExpires(t *testing.T) {
	doctorConfig(t)
	f := healthy()
	f.trust.cert.NotAfter = time.Date(2036, 10, 13, 0, 0, 0, 0, time.UTC)
	out, _ := execute(t, newDoctorCmd(f.backend()), "doctor")
	if !strings.Contains(out, "ok    local CA         trusted by the system trust store; expires 2036-10-13\n") {
		t.Errorf("doctor printed:\n%s", out)
	}
}

func TestDoctorChecksThatAgentsReachNearhostFromEachServer(t *testing.T) {
	doctorConfig(t)
	f := healthy()
	b := f.backend()
	var asked []string
	refusal := errors.New("the server refused to forward /home/dev/.nearhost/doctor-1.sock: remote port forwarding failed")
	b.checkAgents = func(_ context.Context, srv config.Server) error {
		asked = append(asked, srv.Name)
		return nil
	}
	out, err := execute(t, newDoctorCmd(b), "doctor")
	if err != nil || !strings.Contains(out, "ok    agents on mini   can reach Nearhost through ~/.nearhost\n") {
		t.Fatalf("err %v:\n%s", err, out)
	}

	b.checkAgents = func(context.Context, config.Server) error { return refusal }
	out, err = execute(t, newDoctorCmd(b), "doctor")
	if err != nil {
		t.Fatalf("a refusal failed doctor: %v", err)
	}
	if !strings.Contains(out, "warn  agents on mini   "+refusal.Error()+"\n") ||
		!strings.Contains(out, "fix: set AllowStreamLocalForwarding yes in the sshd_config of mini.lan") {
		t.Fatalf("doctor printed:\n%s", out)
	}

	// A server ssh cannot reach is not asked about agents.
	f.serverErr = map[string]error{"mini": errors.New("no route to host")}
	asked = nil
	b = f.backend()
	b.checkAgents = func(_ context.Context, srv config.Server) error { asked = append(asked, srv.Name); return nil }
	execute(t, newDoctorCmd(b), "doctor")
	if len(asked) != 0 {
		t.Fatalf("asked %v", asked)
	}
}

func TestDoctorSuitsTheAgentsFixToTheProblem(t *testing.T) {
	doctorConfig(t)
	b := healthy().backend()
	b.checkAgents = func(context.Context, config.Server) error {
		return &transport.AgentError{Problem: transport.AgentNotReady, Msg: "make /home/dev/.nearhost: permission denied"}
	}
	out, _ := execute(t, newDoctorCmd(b), "doctor")
	if !strings.Contains(out, "warn  agents on mini   make /home/dev/.nearhost: permission denied") ||
		!strings.Contains(out, "can write ~/.nearhost") || strings.Contains(out, "AllowStreamLocalForwarding") {
		t.Fatalf("doctor printed:\n%s", out)
	}
}

func TestDoctorsSocketIsAsLongAsAnInstallsAndNeverOne(t *testing.T) {
	for range 20 {
		name := doctorSocketName()
		if len(name) != len("k3f9q2.sock") || name[0] != '0' || !strings.HasSuffix(name, ".sock") {
			t.Fatalf("doctor's socket %q", name)
		}
	}
}

// A name DNS already sends to loopback needs no hosts entry, so the check
// passes on a block that holds only the .test alias, and asks for the entry
// once DNS stops saying so.
func TestDoctorHostsCheckLeavesOutNamesDNSSendsToLoopback(t *testing.T) {
	path := configHome(t)
	real := localProject("app", 8001, true)
	real.Alias = "app.local.example.io"
	saveConfig(t, path, &config.Config{Projects: []config.Project{localProject("api", 8000, true), real}})
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 api.test\n# nearhost:end\n"
	f.lookup = func(_ context.Context, name string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	out, _ := execute(t, newDoctorCmd(f.backend()), "doctor")
	wantContains(t, out, "ok    hosts block      1 alias\n")

	f.lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, nil } // NXDOMAIN
	out, _ = execute(t, newDoctorCmd(f.backend()), "doctor")
	wantContains(t, out, "FAIL  hosts block      missing app.local.example.io\n")
}
