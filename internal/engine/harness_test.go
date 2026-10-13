package engine

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestMain points every directory Caddy or ssh might touch at a throwaway
// location before any Caddy code runs, so tests never write into the real
// user's directories.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nearhost-engine-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_DATA_HOME", dir)
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeSSH is the fake OpenSSH client shared with the transport tests.
func fakeSSH(t *testing.T) string {
	t.Helper()
	fake, err := filepath.Abs("../transport/testdata/fakessh/ssh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	return fake
}

// authWrapper writes an ssh stand-in that fails authentication for the host
// "badhost", and for every host while the returned flag file exists, and
// otherwise behaves like the fake ssh.
func authWrapper(t *testing.T) (path, flag string) {
	t.Helper()
	dir := t.TempDir()
	path, flag = filepath.Join(dir, "ssh"), filepath.Join(dir, "auth-fails")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ \"$last\" = badhost ] || [ -e '" + flag + "' ]; then FAKE_SSH_AUTH_FAIL=1; export FAKE_SSH_AUTH_FAIL; fi\n" +
		"exec '" + fakeSSH(t) + "' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, flag
}

type harness struct {
	eng       *Engine
	opts      Options
	httpsPort int
	httpPort  int
	storage   string
	runDir    string
	sshLog    string

	mu       sync.Mutex
	ensured  [][]string
	hostsErr error
}

// buildHarness creates an engine that uses the fake ssh, free high ports, temp
// directories and a recording hosts function. It does not start the engine.
func buildHarness(t *testing.T, cfg *config.Config, mutate func(*Options), sshEnv ...string) *harness {
	t.Helper()
	h := &harness{
		httpsPort: proxyPort(t),
		httpPort:  proxyPort(t),
		storage:   tempDir(t, "nh-caddy-"),
		runDir:    tempDir(t, "nh-run-"),
		sshLog:    filepath.Join(t.TempDir(), "ssh.log"),
	}
	h.opts = Options{
		RunDir:      h.runDir,
		StorageRoot: h.storage,
		SSHPath:     fakeSSH(t),
		SSHEnv:      append([]string{"FAKE_SSH_LOG=" + h.sshLog}, sshEnv...),
		HTTPSPort:   h.httpsPort,
		HTTPPort:    h.httpPort,
		EnsureHosts: func(aliases []string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.ensured = append(h.ensured, append([]string(nil), aliases...))
			return h.hostsErr
		},
	}
	if mutate != nil {
		mutate(&h.opts)
	}
	h.eng = New(cfg, h.opts)
	// Tests that care about the holder of a port set their own lookup.
	h.eng.holderOf = func(int) string { return "" }
	t.Cleanup(func() {
		if err := h.eng.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		h.assertNothingLeft(t)
	})
	return h
}

func newHarness(t *testing.T, cfg *config.Config, sshEnv ...string) *harness {
	t.Helper()
	h := buildHarness(t, cfg, nil, sshEnv...)
	if err := h.eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

// proxyPort returns a port free for both TCP and UDP: the proxy also listens
// for HTTP/3 on the UDP port matching its HTTPS port.
func proxyPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		port := testutil.FreePort(t)
		pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		pc.Close()
		return port
	}
	t.Fatal("no port free for both TCP and UDP")
	return 0
}

// tempDir is t.TempDir with a short path that pgrep can take as a pattern
// (for control sockets) and a patient removal: Caddy issues certificates in
// the background and can still be writing one into its storage for a moment
// after it has stopped, which makes a single RemoveAll fail.
func tempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testutil.WaitUntil(t, 10*time.Second, func() bool { return os.RemoveAll(dir) == nil }, "removal of "+dir)
	})
	return dir
}

// assertNothingLeft checks that after Shutdown no engine or session goroutine,
// no ssh process and no proxy listener is left.
func (h *harness) assertNothingLeft(t *testing.T) {
	t.Helper()
	stacks := engineGoroutines()
	for deadline := time.Now().Add(5 * time.Second); stacks != "" && time.Now().Before(deadline); stacks = engineGoroutines() {
		time.Sleep(20 * time.Millisecond)
	}
	if stacks != "" {
		t.Errorf("engine or session goroutines still running after Shutdown:\n%s", stacks)
	}
	if _, err := exec.LookPath("pgrep"); err == nil {
		testutil.WaitUntil(t, 5*time.Second, func() bool {
			out, _ := exec.Command("pgrep", "-f", h.runDir).Output()
			return strings.TrimSpace(string(out)) == ""
		}, "ssh processes to exit")
	}
	for _, port := range []int{h.httpsPort, h.httpPort} {
		if portOpen(port) {
			t.Errorf("something still listens on proxy port %d after Shutdown", port)
		}
	}
	if h.eng.proxy.Running() {
		t.Error("proxy still running after Shutdown")
	}
}

// engineGoroutines returns the stacks of goroutines running engine or session
// code, other than the calling test's own.
func engineGoroutines() string {
	buf := make([]byte, 4<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "engine.(*Engine)") || strings.Contains(g, "transport.(*Session)") {
			out = append(out, g)
		}
	}
	return strings.Join(out, "\n\n")
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func (h *harness) log() string {
	b, _ := os.ReadFile(h.sshLog)
	return string(b)
}

func (h *harness) ensureCalls() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]string(nil), h.ensured...)
}

func (h *harness) setHostsErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hostsErr = err
}

func (h *harness) client(t *testing.T, alias string) *http.Client {
	t.Helper()
	var root *x509.Certificate
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		c, err := proxy.RootCert(h.storage)
		root = c
		return err == nil
	}, "root cert")
	return testutil.HTTPSClient(t, root, alias, h.httpsPort)
}

// get makes one request through the proxy. On a transport error the status is
// 0 and the body is the error text.
func (h *harness) get(t *testing.T, alias, path string) (int, string) {
	t.Helper()
	resp, err := h.client(t, alias).Get(fmt.Sprintf("https://%s:%d%s", alias, h.httpsPort, path))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// getEventually retries until a response arrives. Caddy issues a site's leaf
// certificate after the configuration is applied, so the first handshakes
// after a start can fail.
func (h *harness) getEventually(t *testing.T, alias, path string) (int, string) {
	t.Helper()
	var code int
	var body string
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		code, body = h.get(t, alias, path)
		return code != 0
	}, "HTTPS response for "+alias)
	return code, body
}

// upstream starts a local HTTP server answering "<name>:<host>" and returns
// its port.
func upstream(t *testing.T, name string) int {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name+":"+r.Host)
	}))
	t.Cleanup(up.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func localProject(name string, port int) config.Project {
	return config.Project{
		Name: name, Server: config.LocalServer, Path: "/tmp/" + name, Alias: name + ".test", Enabled: true,
		Routes: []config.Route{{Port: port, Role: config.RoleWeb, Label: "web"}},
	}
}

func remoteProject(name, server string, loopback int) config.Project {
	return config.Project{
		Name: name, Server: server, Path: "/srv/" + name, Alias: name + ".test", Enabled: true,
		Routes: []config.Route{
			{Port: 80, Role: config.RoleWeb},
			{Port: loopback, Role: config.RoleLoopback},
			{Port: 3306, Role: config.RoleIgnore},
		},
	}
}

// remoteConfig is one project "myapp" on server "mini" with a web route, a
// loopback route on the given port and an ignored route.
func remoteConfig(loopback int) *config.Config {
	return &config.Config{
		Servers:  []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{remoteProject("myapp", "mini", loopback)},
	}
}

// forwards lists the forwards the named server's session tracks.
func (h *harness) forwards(server string) [][2]int {
	h.eng.mu.Lock()
	c := h.eng.servers[server]
	h.eng.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.sess.Forwards()
}
