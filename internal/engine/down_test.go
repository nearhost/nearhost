package engine

import (
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
		a.mu.Lock()
		a.last = sites
		a.mu.Unlock()
		return inner(sites)
	}
	return a
}

// lastOf reports whether the site of project the proxy was last given is
// down with no upstream, or up with one.
func (a *appliedSites) lastOf(project string) (down, up bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.last {
		if s.Project == project {
			return s.Down && s.Upstream == "", !s.Down && s.Upstream != ""
		}
	}
	return false, false
}

// While a project's server reconnects, its site is published with no
// upstream, still naming its app for the page; once the server is back, the
// upstream returns. A local project's site is never down.
func TestSiteOfADegradedProjectHasNoUpstream(t *testing.T) {
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
	if s := sites[0]; !s.Down || s.Upstream != "" || s.Project != "myapp" || s.Port != 80 || s.Server != "mini" {
		t.Errorf("degraded: site %+v, want down with no upstream", s)
	}
	if s := sites[1]; s.Down || s.Forwarded || s.Upstream == "" {
		t.Errorf("a local project's site changed with the server: %+v", s)
	}
	// Published to the proxy too, by the engine itself.
	testutil.WaitUntil(t, 10*time.Second, func() bool { down, _ := applied.lastOf("myapp"); return down }, "the proxy to get the site with no upstream")

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	sites = h.eng.Sites()
	if len(sites) != 2 || sites[0].Down || sites[0].Upstream != dial {
		t.Fatalf("reconnected: sites %+v", sites)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { _, up := applied.lastOf("myapp"); return up }, "the proxy to get the upstream back")
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
	if s := h.eng.Sites(); len(s) != 1 || !s[0].Down || s[0].Upstream != "" {
		t.Fatalf("refused web forward: sites %+v, want down with no upstream", s)
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
