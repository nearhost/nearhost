package proxy

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
// runner, so the default 500 reloads catch it on most runs there. On Linux
// 4% failed under the race detector with two CPUs, which 500 reloads catch
// on every run, but 0.06% without it (3 of 5000): CI's main run has no race
// detector, and there 500 reloads catch it on about one run in four. The
// test that always catches it is TestAReloadWaitsForTheOldListeners.
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

// A request made on a new connection while a reload runs is served, by the
// configuration the reload replaces or by the one it loads. Several clients
// keep making such requests throughout a run of reloads, and each reload
// waits until every client has finished a request begun since the one
// before, so requests and reloads overlap on every run whatever the
// machine's speed.
func TestARequestDuringAReloadIsServed(t *testing.T) {
	const clients, reloads = 4, 100
	// A cap on the requests a client makes, so a reload that never returns
	// cannot keep the clients going: each request is bounded by the
	// client's timeout.
	const maxRequests = 50 * reloads
	// A reload waits requestWait at most for the old servers' connections
	// to have their requests read. On a slow runner, or under the race
	// detector, a client's TLS handshake can take longer than the usual
	// second, and the reload would then stop waiting for it and drop its
	// request: that is the bound working, not the loss this test is
	// about. Wait as long as a client's own timeout instead, so a failure
	// here is the loss itself.
	orig := requestWait
	requestWait = 10 * time.Second
	t.Cleanup(func() { requestWait = orig })
	upA, upB := upstream(t, "a"), upstream(t, "b")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(upA.Server)}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(upB.Server)}
	h := startProxy(t, []Site{a, b})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	c := h.client("b.test", false)
	h.getEventually(c, "b.test", "/hello")

	var (
		stop     atomic.Bool
		finished [clients]atomic.Int64 // requests each client has finished
		made     atomic.Int64
		failed   atomic.Int64
		progress = make(chan struct{}, 1) // a client has finished a request
		mu       sync.Mutex
		first    []string // the first few failures
		wg       sync.WaitGroup
	)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < maxRequests && !stop.Load(); n++ {
				err := getHello(c, h.url("b.test", "/hello"))
				made.Add(1)
				if err != nil {
					failed.Add(1)
					mu.Lock()
					if len(first) < 3 {
						first = append(first, err.Error())
					}
					mu.Unlock()
				}
				finished[i].Add(1)
				select {
				case progress <- struct{}{}:
				default:
				}
			}
		}()
	}
	done := func() [clients]int64 {
		var out [clients]int64
		for i := range finished {
			out[i] = finished[i].Load()
		}
		return out
	}
	for i := 0; i < reloads; i++ {
		sites := []Site{b}
		if i%2 == 1 {
			sites = []Site{a, b}
		}
		before := done()
		if err := h.p.Apply(sites); err != nil {
			stop.Store(true)
			wg.Wait()
			t.Fatal(err)
		}
		// Each client finishes one request begun after the reload started,
		// or has stopped at its cap, before the next reload.
		for j := 0; j < clients; {
			if n := finished[j].Load(); n >= before[j]+2 || n >= maxRequests {
				j++
				continue
			}
			<-progress
		}
	}
	stop.Store(true)
	wg.Wait()
	for _, f := range first {
		t.Error(f)
	}
	if n := failed.Load(); n > 0 {
		t.Errorf("%d of %d requests made during %d reloads failed", n, made.Load(), reloads)
	}
}

// getHello makes a GET of url with c and reports how it was not answered
// with b's hello.
func getHello(c *http.Client, url string) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from b" {
		return fmt.Errorf("status %d, body %q", resp.StatusCode, body)
	}
	return nil
}
