package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
)

// Listening on loopback only keeps Windows from asking the user to let
// Nearhost through the firewall.
func TestServersListenOnLoopbackOnWindows(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}},
		Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, WebPort: 6327})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string `json:"listen"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	servers := cfg.Apps.HTTP.Servers
	if len(servers) != 3 {
		t.Fatalf("servers = %v, want the HTTPS, HTTP and web servers", servers)
	}
	ports := map[string]int{"nearhost": 8443, "nearhost_http": 8080, "nearhost_web": 6327}
	for name, s := range servers {
		port, ok := ports[name]
		if !ok {
			t.Errorf("unexpected server %s", name)
			continue
		}
		if want := netbind.ServeAddrs(port); !slices.Equal(s.Listen, want) {
			t.Errorf("server %s listens on %q, want %q", name, s.Listen, want)
		}
		if !slices.Contains(s.Listen, fmt.Sprintf("127.0.0.1:%d", port)) {
			t.Errorf("server %s listens on %q, without 127.0.0.1", name, s.Listen)
		}
	}
}

// Every configuration shares the one socket of a port on Windows, and a
// reload used to leave the stopping server's accept waiting on it: a
// connection made just after the reload could reach that server, which read
// the request and closed the connection unanswered, as net/http does while
// shutting down. Each request here is on a new connection, made as soon as
// a reload returns.
func TestARequestJustAfterAReloadIsServedOnWindows(t *testing.T) {
	upA, upB := upstream(t, "a"), upstream(t, "b")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(upA.Server)}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(upB.Server)}
	h := startProxy(t, []Site{a, b})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	c := h.client("b.test", false)
	h.getEventually(c, "b.test", "/hello")
	const reloads = 2000
	failed := 0
	for i := 0; i < reloads; i++ {
		sites := []Site{b}
		if i%2 == 1 {
			sites = []Site{a, b}
		}
		if err := h.p.Apply(sites); err != nil {
			t.Fatal(err)
		}
		resp, err := c.Get(h.url("b.test", "/hello"))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) == "hello from b" {
				continue
			}
			err = fmt.Errorf("status %d, body %q", resp.StatusCode, body)
		}
		if failed++; failed <= 3 {
			t.Errorf("reload %d: %v", i, err)
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d requests made just after a reload failed", failed, reloads)
	}
}

// Each server hands its connections over through Handoff, before TLS, so
// a stopping server is given none (see Handoff).
func TestServersHandConnectionsToTheNewestConfigOnWindows(t *testing.T) {
	for name, w := range serverWrappers(t) {
		if !slices.Equal(w, []string{handoffModule, "tls"}) {
			t.Errorf("server %s has listener wrappers %q, want %q then tls", name, w, handoffModule)
		}
	}
}
