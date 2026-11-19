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
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// countApplies counts the times h's engine gives the proxy its sites, by
// either of the engine's seams to it.
func countApplies(h *harness) *atomic.Int32 {
	var n atomic.Int32
	apply, refresh := h.eng.applySites, h.eng.refreshSites
	h.eng.applySites = func(sites []proxy.Site) error { n.Add(1); return apply(sites) }
	h.eng.refreshSites = func(sites []proxy.Site) error { n.Add(1); return refresh(sites) }
	return &n
}

// While a project's server reconnects, its gate is shut: the proxy dials
// nothing for it. Once the server is back, the gate is open again. Neither
// reloads the proxy, so other sites are left alone. A local project has no
// gate.
func TestDropAndReconnectShutAndOpenTheGateWithoutReloading(t *testing.T) {
	ssh, flag := downWrapper(t)
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "web")))
	h := buildHarness(t, cfg, func(o *Options) { o.SSHPath = ssh })
	applies := countApplies(h)
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	mustStart(t, h, "web")
	dial := forwardAddr(h.eng.Status("myapp").WebLocalPort)
	sites := h.eng.Sites()
	if len(sites) != 2 || !sites[0].Forwarded || sites[0].Upstream != dial || sites[1].Forwarded {
		t.Fatalf("running: sites %+v", sites)
	}
	if !proxy.ForwardUp(dial) {
		t.Fatal("the gate of a running project is shut")
	}
	before := applies.Load()

	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	waitState(t, h, "myapp", StateDegraded)
	if proxy.ForwardUp(dial) {
		t.Fatal("the gate is open while the server reconnects")
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	if !proxy.ForwardUp(dial) {
		t.Fatal("the gate is shut once the server is back")
	}
	if n := applies.Load() - before; n != 0 {
		t.Errorf("the drop and the reconnect reloaded the proxy %d times", n)
	}
	if s := h.eng.Sites(); len(s) != 2 || s[0].Upstream != dial {
		t.Errorf("reconnected: sites %+v", s)
	}
}

// A web forward the session could not apply again after a reconnect, as its
// port was taken meanwhile, keeps its gate shut: the proxy must not send the
// project's requests to whatever holds the port. A lost loopback forward
// leaves the web route's gate open. A reconnect that applies it opens it.
func TestGateOfARefusedForwardIsShut(t *testing.T) {
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
	if !proxy.ForwardUp(forwardAddr(local)) {
		t.Fatal("a lost loopback forward shut the web route's gate")
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
	if proxy.ForwardUp(forwardAddr(local)) {
		t.Fatal("the gate of a refused web forward is open")
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ReconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	if !proxy.ForwardUp(forwardAddr(local)) {
		t.Fatal("the gate is shut once the forward is restored")
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
			if !proxy.ForwardUp(addr) {
				t.Fatal("the gate is shut once reconnected")
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
// program has taken its port. That route's gate is shut and the proxy dials
// nothing for it, while the project's other web route, whose forward lives,
// is still dialled.
func TestPerProcessSiteDialsNothingWhileItsForwardIsDown(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	flag := filepath.Join(t.TempDir(), "forward-fails")
	h := newHarness(t, remoteTwoWebConfig(testutil.FreePort(t)), append(env, "FAKE_SSH_FORWARD_FAIL_FILE="+flag)...)
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
	testutil.WaitUntil(t, 10*time.Second, func() bool { return !proxy.ForwardUp(forwardAddr(l80)) }, "the gate of the dead forward to shut")
	if !proxy.ForwardUp(forwardAddr(l81)) {
		t.Error("the gate of the forward that lives was shut")
	}

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
// tries again. Its project's gate is shut meanwhile, and open once it is
// back.
func TestGateOfASlowRetryServerIsShut(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	dial := forwardAddr(h.eng.Status("myapp").WebLocalPort)
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to wait before it tries again")
	if proxy.ForwardUp(dial) {
		t.Fatal("the gate is open while the server waits")
	}
	h.getEventually(t, "myapp.test", "/")
	if code, body := h.browserGet(t, "myapp.test"); code != 502 || !strings.Contains(body, "Nearhost is reconnecting to mini.") {
		t.Errorf("while the server waits: %d\n%s", code, body)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp") // revives the connection
	waitState(t, h, "myapp", StateRunning)
	if !proxy.ForwardUp(dial) {
		t.Fatal("the gate is shut once the server is back")
	}
}
