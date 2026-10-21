package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
)

// upRun is one `nearhost up` running in the background of a test, with fake
// ssh, free high ports, a recording hosts function and a context in place of
// signals.
type upRun struct {
	t         *testing.T
	cfgPath   string
	httpsPort int
	httpPort  int
	webPort   int
	sshEnv    []string
	hostsErr  error
	// hostsBlocks makes the hosts function wait until its context ends.
	hostsBlocks bool
	noHosts     bool
	// trust stands for the system trust store. It trusts the local CA unless
	// a test says otherwise.
	trust *fakeTrust

	mu      sync.Mutex
	ensured [][]string
	again   chan struct{} // closed for the second signal; set once up listens

	out    syncBuffer
	cancel context.CancelFunc
	done   chan error
}

func newUpRun(t *testing.T, cfg *config.Config) *upRun {
	t.Helper()
	u := &upRun{t: t, cfgPath: configHome(t), trust: newFake()}
	u.trust.trusted = true
	u.httpsPort, u.httpPort = testutil.ProxyPorts(t)
	// Not the default web port, which a Nearhost running on this machine
	// holds.
	u.webPort = testutil.ProxyPort(t)
	saveConfig(t, u.cfgPath, cfg)
	return u
}

func (u *upRun) backend() upBackend {
	return upBackend{
		ssh: sshClient{path: testutil.FakeSSH(u.t), env: append([]string{"FAKE_SSH_LOG=" + filepath.Join(filepath.Dir(u.cfgPath), "ssh.log")}, u.sshEnv...)},
		ensureHosts: func(ctx context.Context, aliases []string) error {
			u.mu.Lock()
			u.ensured = append(u.ensured, append([]string(nil), aliases...))
			hostsErr, blocks := u.hostsErr, u.hostsBlocks
			u.mu.Unlock()
			if blocks {
				<-ctx.Done() // as sudo waiting at its password prompt
				return ctx.Err()
			}
			return hostsErr
		},
		trust: u.trust.backend(),
		// The command's own context stands for the first signal;
		// interruptAgain sends the second.
		notify: func(parent context.Context) (context.Context, <-chan struct{}, func()) {
			ctx, cancel := context.WithCancel(parent)
			again := make(chan struct{})
			u.mu.Lock()
			u.again = again
			u.mu.Unlock()
			return ctx, again, cancel
		},
	}
}

func (u *upRun) args(extra ...string) []string {
	args := []string{"up", "--https-port", strconv.Itoa(u.httpsPort), "--http-port", strconv.Itoa(u.httpPort), "--web-port", strconv.Itoa(u.webPort)}
	if u.noHosts {
		args = append(args, "--no-hosts")
	}
	return append(args, extra...)
}

// start runs the command until the test ends or stop is called.
func (u *upRun) start(extra ...string) {
	u.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	u.done = make(chan error, 1)
	go func() { u.done <- executeInto(ctx, &u.out, newUpCmd(u.backend()), u.args(extra...)...) }()
	u.t.Cleanup(func() {
		if u.done != nil {
			u.stop()
		}
	})
}

// stop interrupts the command as a signal would and checks that nothing of it
// is left: no listener on the proxy ports, no ssh process, no engine goroutine.
func (u *upRun) stop() error {
	u.t.Helper()
	u.cancel()
	var err error
	select {
	case err = <-u.done:
	case <-time.After(60 * time.Second):
		u.t.Fatalf("up did not return after its context ended; output:\n%s", u.out.String())
	}
	u.done = nil
	for _, port := range []int{u.httpsPort, u.httpPort, u.webPort} {
		if portOpen(port) {
			u.t.Errorf("something still listens on port %d after up returned", port)
		}
	}
	runDir := filepath.Join(filepath.Dir(u.cfgPath), "run")
	if _, lookErr := exec.LookPath("pgrep"); lookErr == nil {
		testutil.WaitUntil(u.t, 5*time.Second, func() bool {
			out, _ := exec.Command("pgrep", "-f", runDir).Output()
			return strings.TrimSpace(string(out)) == ""
		}, "ssh processes to exit")
	}
	testutil.WaitUntil(u.t, 5*time.Second, func() bool { return leftoverGoroutines() == "" }, "engine, session and up goroutines to end")
	return err
}

// interruptAgain stands for a second signal, sent while up is shutting down.
func (u *upRun) interruptAgain() {
	u.t.Helper()
	var again chan struct{}
	testutil.WaitUntil(u.t, 10*time.Second, func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		again = u.again
		return again != nil
	}, "up to listen for signals")
	close(again)
}

// leftoverGoroutines returns the stacks of goroutines still running engine,
// session or command code.
func leftoverGoroutines() string {
	buf := make([]byte, 4<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "engine.(*Engine)") || strings.Contains(g, "transport.(*Session)") || strings.Contains(g, "cli.runUp") {
			out = append(out, g)
		}
	}
	return strings.Join(out, "\n\n")
}

func (u *upRun) waitFor(sub string) {
	u.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(u.out.String(), sub) {
			return
		}
		select {
		case err := <-u.done:
			u.done <- err
			if !strings.Contains(u.out.String(), sub) {
				u.t.Fatalf("up returned (%v) before printing %q; output:\n%s", err, sub, u.out.String())
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	u.t.Fatalf("timed out waiting for %q; output:\n%s", sub, u.out.String())
}

func (u *upRun) ensureCalls() [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]string(nil), u.ensured...)
}

// get polls the proxy until it answers for alias: Caddy issues the leaf
// certificate after the site is published.
func (u *upRun) get(alias string) string {
	u.t.Helper()
	storage, err := config.CaddyDir()
	if err != nil {
		u.t.Fatal(err)
	}
	root, err := proxy.RootCert(storage)
	if err != nil {
		u.t.Fatalf("root certificate: %v", err)
	}
	client := testutil.HTTPSClient(u.t, root, alias, u.httpsPort)
	var body string
	testutil.WaitUntil(u.t, 15*time.Second, func() bool {
		resp, err := client.Get(fmt.Sprintf("https://%s:%d/", alias, u.httpsPort))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		body = string(b)
		return err == nil
	}, "an HTTPS response for "+alias)
	return body
}

func localProject(name string, port int, enabled bool) config.Project {
	return config.Project{
		Name: name, Server: config.LocalServer, Path: "/tmp/" + name, Alias: name + ".test", Enabled: enabled,
		Routes: []config.Route{{Port: port, Role: config.RoleWeb}},
	}
}

func remoteProject(name string) config.Project {
	return config.Project{
		Name: name, Server: "mini", Path: "/srv/" + name, Alias: name + ".test", Enabled: true,
		Routes: []config.Route{{Port: 80, Role: config.RoleWeb}},
	}
}

var mini = []config.Server{{Name: "mini", SSHHost: "mini.lan"}}
