package engine

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// TestMain points every directory Caddy or ssh might touch at a throwaway
// location before any Caddy code runs, so tests never write into the real
// user's directories.
// It also unsets NEARHOST_TRANSPORT, so the tests get the platform's own
// transport whatever the developer's shell asks for: ControlMaster on Unix,
// per-process on Windows, which has no ControlMaster. Tests that only make
// sense on ControlMaster are in the _unix_test.go files.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nearhost-engine-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_DATA_HOME", dir)
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("HOME", dir)
	os.Setenv("USERPROFILE", dir) // os.UserHomeDir on Windows
	os.Unsetenv("NEARHOST_TRANSPORT")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeSSH is the fake OpenSSH client shared with the transport tests.
func fakeSSH(t *testing.T) string {
	t.Helper()
	return testutil.FakeSSH(t)
}

// authWrapper returns the fake ssh set, for the rest of the test, to fail
// authentication for the host "badhost", and for every host while the flag
// file exists. While the hold file exists, a master for "badhost" waits
// before it fails, so a test decides when the failure comes.
func authWrapper(t *testing.T) (path, flag, hold string) {
	t.Helper()
	dir := t.TempDir()
	flag, hold = filepath.Join(dir, "auth-fails"), filepath.Join(dir, "auth-waits")
	t.Setenv("FAKE_SSH_AUTH_FAIL_HOST", "badhost")
	t.Setenv("FAKE_SSH_AUTH_FAIL_FILE", flag)
	t.Setenv("FAKE_SSH_AUTH_HOLD", hold)
	return fakeSSH(t), flag, hold
}

type harness struct {
	eng       *Engine
	opts      Options
	httpsPort int
	httpPort  int
	storage   string
	runDir    string
	sshLog    string
	// links is where the per-process fake ssh reports its processes and
	// is told to drop one; a test's own newPerProcessFake overrides it.
	links *perProcessFake

	mu       sync.Mutex
	ensured  [][]string
	hostsErr error
	// events holds every event since the harness was built; seen is what
	// transportLines has read of it.
	events <-chan Event
	seen   []Event
}

// buildHarness creates an engine that uses the fake ssh, free high ports, temp
// directories and a recording hosts function. It does not start the engine.
func buildHarness(t *testing.T, cfg *config.Config, mutate func(*Options), sshEnv ...string) *harness {
	t.Helper()
	// Two different ports from outside the range the system hands out, so
	// nothing takes one between here and the engine binding it.
	httpsPort, httpPort := testutil.ProxyPorts(t)
	h := &harness{
		httpsPort: httpsPort,
		httpPort:  httpPort,
		storage:   tempDir(t, "nh-caddy-"),
		runDir:    tempDir(t, "nh-run-"),
		sshLog:    filepath.Join(t.TempDir(), "ssh.log"),
	}
	links, linkEnv := newPerProcessFake(t)
	h.links = links
	h.opts = Options{
		RunDir:      h.runDir,
		StorageRoot: h.storage,
		SSHPath:     fakeSSH(t),
		SSHEnv:      append(append([]string{"FAKE_SSH_LOG=" + h.sshLog}, linkEnv...), sshEnv...),
		HTTPSPort:   h.httpsPort,
		HTTPPort:    h.httpPort,
		EnsureHosts: func(_ context.Context, aliases []string) error {
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
	h.eng.holderOf = func(context.Context, int) string { return "" }
	events, unsub := h.eng.Events().Subscribe(1 << 16)
	h.events = events
	t.Cleanup(func() {
		unsub()
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

// tempDir is a short temporary directory with a patient removal.
func tempDir(t *testing.T, prefix string) string {
	t.Helper()
	return testutil.TempDir(t, prefix)
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

// engineGoroutines returns the stacks of goroutines running engine or
// transport code, other than the calling test's own. The per-process
// transport waits for each ssh it starts in a goroutine of its own, so none
// left means none of its ssh processes is either.
func engineGoroutines() string {
	buf := make([]byte, 4<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "engine.(*Engine)") || strings.Contains(g, "transport.(*Session)") || strings.Contains(g, "transport.(*perProcess)") {
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
	return testutil.Upstream(t, name)
}

func localProject(name string, port int) config.Project {
	return config.Project{
		Name: name, Server: config.LocalServer, Path: "/tmp/" + name, Alias: name + ".test", Enabled: true,
		Routes: []config.Route{{Port: port, Role: config.RoleWeb, Label: "web"}},
	}
}

// localProjectRoutes is a local project with the given routes.
func localProjectRoutes(name string, routes ...config.Route) config.Project {
	return config.Project{
		Name: name, Server: config.LocalServer, Path: "/tmp/" + name, Alias: name + ".test", Enabled: true,
		Routes: routes,
	}
}

// localConfig is one local project with the given routes.
func localConfig(name string, routes ...config.Route) *config.Config {
	return &config.Config{Projects: []config.Project{localProjectRoutes(name, routes...)}}
}

// mustStart starts a project and fails the test if that fails.
func mustStart(t *testing.T, h *harness, name string) {
	t.Helper()
	if err := h.eng.StartProject(context.Background(), name); err != nil {
		t.Fatalf("StartProject %s: %v", name, err)
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

// dropMaster makes the connection to a server drop, as a lost connection
// would: a ControlMaster's fake master leaves when its socket file
// disappears, and a per-process fake link when it is told to through the
// drop directory.
func (h *harness) dropMaster(t *testing.T, server string) {
	t.Helper()
	h.eng.mu.Lock()
	var sess transport.Transport
	c := h.eng.servers[server]
	if c != nil {
		sess = c.sess
	}
	h.eng.mu.Unlock()
	if c == nil {
		t.Fatalf("dropMaster: nothing is connected to %s", server)
	}
	if s, ok := sess.(*transport.Session); ok {
		if err := os.Remove(s.SocketPath()); err != nil {
			t.Fatal(err)
		}
		return
	}
	h.links.drop(t, h.links.hostLinkPid(t, sess.Host()))
}

// unapplied lists the forwards of a server that its current master lacks.
func (h *harness) unapplied(server string) []int {
	h.eng.mu.Lock()
	c := h.eng.servers[server]
	h.eng.mu.Unlock()
	var out []int
	_, states := c.sess.ForwardStates()
	for _, f := range states {
		if !f.Applied {
			out = append(out, f.Local)
		}
	}
	return out
}

// stateEvents drains what is buffered on events and returns the states
// announced for a project, in order, with the last message of each. A state
// announced again because its reason changed is listed once.
func stateEvents(events <-chan Event, project string) (states []State, last map[State]string) {
	last = map[State]string{}
	for {
		select {
		case ev := <-events:
			if ev.Project == project && ev.State != "" {
				if len(states) == 0 || states[len(states)-1] != ev.State {
					states = append(states, ev.State)
				}
				last[ev.State] = ev.Message
			}
		default:
			return states, last
		}
	}
}

// waitStateEvent waits for a state event of project carrying exactly message
// and fails the test if none arrives. Earlier events are dropped.
func waitStateEvent(t *testing.T, events <-chan Event, project, message string) {
	t.Helper()
	var seen []string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project != project || ev.State == "" {
				continue
			}
			if ev.Message == message {
				return
			}
			seen = append(seen, ev.Message)
		case <-deadline:
			t.Fatalf("no state event %q for %s; got %q", message, project, seen)
		}
	}
}

// transportLines returns the lines the transport of server has logged
// since the harness was built. Both transports log "<server>: forward
// <spec>" when a forward is set up, and "<server>: cancel <spec>" when one
// is cancelled, so these say what was forwarded on either.
func (h *harness) transportLines(server string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for drained := false; !drained; {
		select {
		case ev, ok := <-h.events:
			if !ok {
				drained = true
				break
			}
			h.seen = append(h.seen, ev)
		default:
			drained = true
		}
	}
	var out []string
	for _, ev := range h.seen {
		if ev.Project == "" && ev.Server == server {
			out = append(out, ev.Message)
		}
	}
	return out
}

// forwarded returns the specs (127.0.0.1:LOCAL:localhost:REMOTE) of the
// forwards set up on server, once for each time one was set up.
func (h *harness) forwarded(server string) []string { return h.specsLogged(server, "forward") }

// cancelled returns the specs of the forwards cancelled on server, once for
// each cancel.
func (h *harness) cancelled(server string) []string { return h.specsLogged(server, "cancel") }

func (h *harness) specsLogged(server, op string) []string {
	var out []string
	for _, line := range h.transportLines(server) {
		spec, ok := strings.CutPrefix(line, server+": "+op+" ")
		if ok && !strings.Contains(spec, " ") {
			out = append(out, spec)
		}
	}
	return out
}

// spec is the forward spec of a local and remote port, as ssh's -L takes it.
func spec(local, remote int) string {
	return fmt.Sprintf("127.0.0.1:%d:localhost:%d", local, remote)
}

// connections counts the ssh connections launched: ControlMaster masters
// (-N -M) or per-process links (-N with no -L), whichever the platform's
// transport runs.
func (h *harness) connections() int {
	n := 0
	for _, line := range strings.Split(h.log(), "\n") {
		argv := " " + line + " "
		if strings.Contains(argv, " -N ") && !strings.Contains(argv, " -L ") {
			n++
		}
	}
	return n
}

// countSpec counts the times want appears in specs.
func countSpec(specs []string, want string) int {
	n := 0
	for _, s := range specs {
		if s == want {
			n++
		}
	}
	return n
}

// connectionEnded reports whether the connection to host was ended: a
// ControlMaster's master is asked to exit with `-O exit`; the per-process
// transport ends its own ssh processes, and has ended them all once none of
// its goroutines, each waiting for one of them, is left.
func (h *harness) connectionEnded(host string) bool {
	if runtime.GOOS != "windows" && os.Getenv("NEARHOST_TRANSPORT") != "per-process" {
		return strings.Contains(h.log(), "-O exit -- "+host)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !strings.Contains(engineGoroutines(), "transport.(*perProcess)") {
			return true
		}
	}
	return false
}

// perProcessTransport reports whether the engine's servers get the
// per-process transport, as transport.New decides: always on Windows, and
// on Unix when NEARHOST_TRANSPORT asks for it.
func perProcessTransport() bool {
	return runtime.GOOS == "windows" || os.Getenv("NEARHOST_TRANSPORT") == "per-process"
}

// heldTransport wraps a server's transport so that its CancelForward waits
// while it is held, before it cancels anything, on either transport. It
// counts the cancels asked of it.
type heldTransport struct {
	transport.Transport
	mu    sync.Mutex
	gate  chan struct{} // nil while not held
	asked atomic.Int32
}

func (w *heldTransport) CancelForward(ctx context.Context, local, remote int) error {
	w.asked.Add(1)
	w.mu.Lock()
	gate := w.gate
	w.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return w.Transport.CancelForward(ctx, local, remote)
}

// hold makes the cancels asked from now on wait until release.
func (w *heldTransport) hold() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gate == nil {
		w.gate = make(chan struct{})
	}
}

// release lets every waiting cancel go on, and those asked later too.
func (w *heldTransport) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gate != nil {
		close(w.gate)
		w.gate = nil
	}
}

// holdCancels puts a heldTransport in place of the transport of server,
// whose connection must exist. A passthrough keeps the transport it was
// opened on, so this comes before any passthrough opens. The hold is
// released when the test ends, before the harness shuts down.
func (h *harness) holdCancels(t *testing.T, server string) *heldTransport {
	t.Helper()
	h.eng.mu.Lock()
	var w *heldTransport
	if c := h.eng.servers[server]; c != nil {
		w = &heldTransport{Transport: c.sess}
		c.sess = w
	}
	h.eng.mu.Unlock()
	if w == nil {
		t.Fatalf("holdCancels: nothing is connected to %s", server)
	}
	t.Cleanup(w.release)
	return w
}
