package proxy

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"
)

// A request made on a new connection as soon as a reload returns is served.
//
// On Windows every configuration shares the one socket of a port, and a
// reload used to leave the stopping server's accept waiting on it: such a
// connection could reach that server, which read the request and closed the
// connection unanswered, as net/http does while shutting down (see Handoff).
// On Unix each configuration binds its own socket, and the reload used to
// return while Caddy had yet to close the old one: the kernel queued such a
// connection on it and reset it at the close (see Retire).
//
// Without the fixes 0.4-2.9% of such requests failed on the Windows CI
// runner, and 4% on Linux under the race detector with two CPUs, far fewer
// without it, so the default 500 reloads catch it on most runs there;
// NEARHOST_RELOAD_TEST_COUNT sets another count, to look harder.
func TestARequestJustAfterAReloadIsServed(t *testing.T) {
	reloads := 500
	if v := os.Getenv("NEARHOST_RELOAD_TEST_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("NEARHOST_RELOAD_TEST_COUNT = %q, want a positive count", v)
		}
		reloads = n
	}
	upA, upB := upstream(t, "a"), upstream(t, "b")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(upA.Server)}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(upB.Server)}
	h := startProxy(t, []Site{a, b})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	c := h.client("b.test", false)
	h.getEventually(c, "b.test", "/hello")
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
