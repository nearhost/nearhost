package proxy

// TEMPORARY: diagnostics for a request just after a reload getting EOF on
// Windows. Removed before the branch merges.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func init() { caddy.RegisterModule(traceWrapper{}) }

type traceWrapper struct {
	Gen int `json:"gen"`
}

func (traceWrapper) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.listeners.nearhost_test_trace", New: func() caddy.Module { return new(traceWrapper) }}
}

func (w traceWrapper) WrapListener(ln net.Listener) net.Listener {
	return &traceListener{Listener: ln, gen: w.Gen}
}

type connTrace struct {
	gen        int
	accepted   time.Time
	closed     time.Time
	closeStack string
}

var (
	tracesMu sync.Mutex
	traces   = map[string]*connTrace{} // remote address to its trace
)

type traceListener struct {
	net.Listener
	gen int
}

func (l *traceListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	tr := &connTrace{gen: l.gen, accepted: time.Now()}
	tracesMu.Lock()
	traces[c.RemoteAddr().String()] = tr
	tracesMu.Unlock()
	return &traceConn{Conn: c, tr: tr}, nil
}

type traceConn struct {
	net.Conn
	tr   *connTrace
	once sync.Once
}

func (c *traceConn) Close() error {
	c.once.Do(func() {
		buf := make([]byte, 16<<10)
		buf = buf[:runtime.Stack(buf, false)]
		tracesMu.Lock()
		c.tr.closed, c.tr.closeStack = time.Now(), string(buf)
		tracesMu.Unlock()
	})
	return c.Conn.Close()
}

// loadTraced loads the config for sites as Apply does, with the https
// server's connections traced as generation gen and Caddy logging at debug.
func loadTraced(t *testing.T, h *harness, sites []Site, gen int) {
	t.Helper()
	raw, err := BuildConfig(sites, h.p.opts)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["nearhost"].(map[string]any)
	srv["listener_wrappers"] = []any{map[string]any{"wrapper": "nearhost_test_trace", "gen": gen}, map[string]any{"wrapper": "tls"}}
	cfg["logging"].(map[string]any)["logs"].(map[string]any)["default"].(map[string]any)["level"] = "DEBUG"
	raw, _ = json.Marshal(cfg)
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if err := caddy.Load(raw, false); err != nil {
		t.Fatal(err)
	}
	owner = h.p
}

func TestTraceRequestJustAfterReload(t *testing.T) {
	var n int
	if v := os.Getenv("NEARHOST_TRACE_RELOADS"); v != "" {
		fmt.Sscan(v, &n)
	}
	if n == 0 {
		t.Skip("set NEARHOST_TRACE_RELOADS to the number of reloads")
	}
	upA, upB := upstream(t, "a"), upstream(t, "b")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(upA.Server)}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(upB.Server)}
	h := startProxy(t, []Site{a, b})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	h.getEventually(h.client("b.test", false), "b.test", "/hello")
	tlsCfg := h.client("b.test", false).Transport.(*http.Transport).TLSClientConfig
	failures, stale := 0, 0
	for gen := 1; gen <= n; gen++ {
		sites := []Site{b}
		if gen%2 == 0 {
			sites = []Site{a, b}
		}
		loadTraced(t, h, sites, gen)
		var local string
		c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig:   tlsCfg,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				c, err := (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", h.httpsPort))
				if err == nil {
					local = c.LocalAddr().String()
				}
				return c, err
			},
		}}
		start := time.Now()
		resp, err := c.Get(h.url("b.test", "/hello"))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && string(body) == "hello from b" {
				tracesMu.Lock()
				if tr := traces[local]; tr != nil && tr.gen != gen {
					stale++
					t.Logf("generation %d: served by generation %d", gen, tr.gen)
				}
				tracesMu.Unlock()
				continue
			}
			err = fmt.Errorf("status %d body %q", resp.StatusCode, body)
		}
		failures++
		end := time.Now()
		time.Sleep(200 * time.Millisecond) // let a late Close land in the trace
		tracesMu.Lock()
		tr := traces[local]
		var desc string
		if tr == nil {
			desc = "accepted by no traced listener"
		} else {
			desc = fmt.Sprintf("accepted by generation %d at %s (the request started %s, ended %s); closed at %s by:\n%s",
				tr.gen, tr.accepted.Format("15:04:05.000000"), start.Format("15:04:05.000000"), end.Format("15:04:05.000000"),
				tr.closed.Format("15:04:05.000000"), tr.closeStack)
		}
		tracesMu.Unlock()
		if failures <= 3 {
			t.Errorf("generation %d (%d sites): GET b.test from %s: %v\n%s\n%s", gen, len(sites), local, err, desc, h.state("b.test"))
		}
	}
	t.Logf("%d of %d requests just after a reload were served by an older generation", stale, n)
	if failures > 0 {
		t.Errorf("%d of %d requests just after a reload failed", failures, n)
	}
}
