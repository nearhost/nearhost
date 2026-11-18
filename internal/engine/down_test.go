package engine

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// appliedSites records the sites the engine gives the proxy outside a start
// or a stop: those it publishes again when a forward goes down or comes
// back.
type appliedSites struct {
	mu   sync.Mutex
	last []proxy.Site
}

// recordApplied wraps h's refreshSites so a test sees what it applies.
func recordApplied(h *harness) *appliedSites {
	a := &appliedSites{}
	inner := h.eng.refreshSites
	h.eng.refreshSites = func(sites []proxy.Site) error {
		err := inner(sites)
		a.mu.Lock()
		a.last = sites // once the proxy runs them
		a.mu.Unlock()
		return err
	}
	return a
}

// lastOf reports whether the site of project's web route for port the
// proxy was last given is down or up.
func (a *appliedSites) lastOf(project string, port int) (down, up bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.last {
		if s.Project == project && s.Port == port {
			return s.Down, !s.Down
		}
	}
	return false, false
}

// While a project's server reconnects, its site is published down, still
// naming its app for the page; once the server is back, it is up again. A
// local project's site is never down.
func TestSiteOfADegradedProjectIsDown(t *testing.T) {
	ssh, flag := downWrapper(t)
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "web")))
	h := buildHarness(t, cfg, func(o *Options) { o.SSHPath = ssh })
	applied := recordApplied(h)
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	mustStart(t, h, "web")
	dial := "127.0.0.1:" + strconv.Itoa(h.eng.Status("myapp").WebLocalPort)
	sites := h.eng.Sites()
	if len(sites) != 2 || sites[0].Down || !sites[0].Forwarded || sites[0].Upstream != dial {
		t.Fatalf("running: sites %+v", sites)
	}

	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	waitState(t, h, "myapp", StateDegraded)
	sites = h.eng.Sites()
	if len(sites) != 2 {
		t.Fatalf("degraded: sites %+v", sites)
	}
	if s := sites[0]; !s.Down || s.Upstream != dial || s.Project != "myapp" || s.Port != 80 || s.Server != "mini" {
		t.Errorf("degraded: site %+v, want down", s)
	}
	if s := sites[1]; s.Down || s.Forwarded || s.Upstream == "" {
		t.Errorf("a local project's site changed with the server: %+v", s)
	}
	// Published to the proxy too, by the engine itself.
	testutil.WaitUntil(t, 10*time.Second, func() bool { down, _ := applied.lastOf("myapp", 80); return down }, "the proxy to get the site down")

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	sites = h.eng.Sites()
	if len(sites) != 2 || sites[0].Down || sites[0].Upstream != dial {
		t.Fatalf("reconnected: sites %+v", sites)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { _, up := applied.lastOf("myapp", 80); return up }, "the proxy to get the site up again")
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusConnected {
		t.Errorf("server %s", st)
	}
}

// A web forward the session could not apply again after a reconnect, as its
// port was taken meanwhile, leaves the site down: the proxy must not send
// the project's requests to whatever holds the port. A lost loopback forward
// leaves the web route up. A reconnect that applies it restores the site.
func TestSiteOfARefusedForwardIsDown(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-fails")
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
	mustStart(t, h, "myapp")
	local := h.eng.Status("myapp").WebLocalPort

	if err := os.WriteFile(flag, []byte(strconv.Itoa(lb)), 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("myapp").State == StateError }, "the lost loopback forward to show")
	if s := h.eng.Sites(); len(s) != 1 || s[0].Down {
		t.Fatalf("a lost loopback forward took the web route down: %+v", s)
	}

	if err := os.WriteFile(flag, []byte(strconv.Itoa(local)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ReconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st := h.eng.Status("myapp")
		return st.State == StateError && strings.Contains(st.Message, "the web route for port 80 is not forwarded")
	}, "the lost web forward to show")
	if s := h.eng.Sites(); len(s) != 1 || !s[0].Down {
		t.Fatalf("refused web forward: sites %+v, want down", s)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ReconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	if s := h.eng.Sites(); len(s) != 1 || s[0].Down || s[0].Upstream != forwardAddr(local) {
		t.Fatalf("restored: sites %+v", s)
	}
}

// stopWatch wraps a server's transport to report, when the engine stops it,
// whether the proxy could still dial the forward at addr.
type stopWatch struct {
	transport.Transport
	addr string
	open atomic.Bool
}

func (w *stopWatch) Stop() {
	// Saying the forward is down changes nothing if it already was.
	w.open.Store(proxy.SetForwardUp(w.addr, false))
	w.Transport.Stop()
}

// A reconnect asked for ends the session, freeing the forwards' ports at
// once: the proxy has stopped dialling them before that. So has a
// disconnect.
func TestReconnectShutsTheGateBeforeEndingTheSession(t *testing.T) {
	for _, how := range []string{"ReconnectServer", "DisconnectServer"} {
		t.Run(how, func(t *testing.T) {
			h := newHarness(t, remoteConfig(testutil.FreePort(t)))
			mustStart(t, h, "myapp")
			addr := forwardAddr(h.eng.Status("myapp").WebLocalPort)
			h.eng.mu.Lock()
			c := h.eng.servers["mini"]
			w := &stopWatch{Transport: c.sess, addr: addr}
			c.sess = w
			h.eng.mu.Unlock()
			if proxy.SetForwardUp(addr, true) {
				t.Fatal("the gate of a running project was shut")
			}
			var err error
			if how == "ReconnectServer" {
				err = h.eng.ReconnectServer("mini")
			} else {
				err = h.eng.DisconnectServer("mini")
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.open.Load() {
				t.Fatal("the session was ended while the proxy could still dial its forward")
			}
			if how == "DisconnectServer" {
				if err := h.eng.ConnectServer("mini"); err != nil {
					t.Fatal(err)
				}
			}
			waitState(t, h, "myapp", StateRunning)
			if s := h.eng.Sites(); len(s) != 1 || s[0].Down {
				t.Fatalf("reconnected: sites %+v", s)
			}
		})
	}
}

// browserGet asks host for a page through the proxy, as a browser does, and
// returns the status and body.
func (h *harness) browserGet(t *testing.T, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s:%d/", host, h.httpsPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := h.client(t, host).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", host, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// countingListener listens on 127.0.0.1:port, as another program that took
// the port would, and counts the connections it accepts.
func countingListener(t *testing.T, port int) *atomic.Int32 {
	t.Helper()
	var l net.Listener
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		var err error
		l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		return err == nil
	}, fmt.Sprintf("port %d to be free", port))
	var n atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	t.Cleanup(func() { l.Close() })
	return &n
}

// Every OS: one web forward's ssh dies and cannot be relaunched, as another
// program has taken its port. That route's site is down and dials nothing,
// while the project's other web route, whose forward lives, is still
// dialled.
func TestPerProcessSiteDialsNothingWhileItsForwardIsDown(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	flag := filepath.Join(t.TempDir(), "forward-fails")
	h := newHarness(t, remoteTwoWebConfig(testutil.FreePort(t)), append(env, "FAKE_SSH_FORWARD_FAIL_FILE="+flag)...)
	applied := recordApplied(h)
	mustStart(t, h, "myapp")
	st := h.eng.Status("myapp")
	l80, l81 := st.WebLocalPorts[80], st.WebLocalPorts[81]
	pids := fake.forwardPids(l80)
	if len(pids) != 1 {
		t.Fatalf("web forward pids %v", pids)
	}

	if err := os.WriteFile(flag, []byte(strconv.Itoa(l80)), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.drop(t, pids[0])
	squatter := countingListener(t, l80)
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		s := h.eng.Sites()
		return len(s) == 2 && s[0].Down
	}, "the route of the dead forward to be down")
	if s := h.eng.Sites(); s[1].Down {
		t.Fatalf("the route whose forward lives is down: %+v", s)
	}
	if proxy.SetForwardUp(forwardAddr(l81), true) {
		t.Error("the gate of the forward that lives was shut")
	}
	// A reload drops the connections that arrive while it swaps the
	// routes, so the requests wait for the one that takes the route down.
	testutil.WaitUntil(t, 10*time.Second, func() bool { down, _ := applied.lastOf("myapp", 80); return down }, "the proxy to get the route down")

	// Caddy issues each certificate once the site is published: the first
	// requests wait for it.
	if c, b := h.getEventually(t, "myapp.test", "/"); c != 502 || b != "" {
		t.Errorf("the route of the dead forward, no Accept: %d %q", c, b)
	}
	code, body := h.browserGet(t, "myapp.test")
	if code != 502 || !strings.Contains(body, "Nearhost is reconnecting to mini.") {
		t.Errorf("the route of the dead forward: %d\n%s", code, body)
	}
	if n := squatter.Load(); n != 0 {
		t.Fatalf("the program on the dead forward's port got %d connections", n)
	}
	// The fake's forward hangs up at once, so the live route is dialled and
	// fails as an app that does not answer, not as a forward that is down.
	h.getEventually(t, "ws.local.example.io", "/")
	code, body = h.browserGet(t, "ws.local.example.io")
	if code != 502 || !strings.Contains(body, fmt.Sprintf("Nothing answered on port %d on mini.", 81)) {
		t.Errorf("the route whose forward lives was not dialled: %d\n%s", code, body)
	}
	if n := squatter.Load(); n != 0 {
		t.Fatalf("the program on the dead forward's port got %d connections", n)
	}
}

// Every OS: a server that refused the logins waits a long while before it
// tries again. Its project's site is down meanwhile, and up once it is
// back.
func TestSiteOfASlowRetryServerIsDown(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	applied := recordApplied(h)
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to wait before it tries again")
	if s := h.eng.Sites(); len(s) != 1 || !s[0].Down {
		t.Fatalf("sites while the server waits: %+v", s)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { down, _ := applied.lastOf("myapp", 80); return down }, "the proxy to get the site down")
	h.getEventually(t, "myapp.test", "/")
	if code, body := h.browserGet(t, "myapp.test"); code != 502 || !strings.Contains(body, "Nearhost is reconnecting to mini.") {
		t.Errorf("while the server waits: %d\n%s", code, body)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp") // revives the connection
	waitState(t, h, "myapp", StateRunning)
	if s := h.eng.Sites(); len(s) != 1 || s[0].Down {
		t.Fatalf("sites once the server is back: %+v", s)
	}
}
