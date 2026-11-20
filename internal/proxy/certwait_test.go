package proxy

import (
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// answersAtOnce makes one request for host, with no second try, and fails
// the test when it is not answered.
func (h *harness) answersAtOnce(host string) {
	h.t.Helper()
	resp, err := h.client(host, false).Get(h.url(host, "/hello"))
	if err != nil {
		h.t.Fatalf("the first request for %s, made as the load returned: %v", host, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// Caddy obtains a certificate after its load has returned. Start waits for
// it, so the first request is answered.
func TestTheFirstRequestAfterAStartIsAnswered(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Hosts: []string{"a.test", "www.a.test"}, Upstream: addr(up.Server)}})
	h.answersAtOnce("a.test")
	h.answersAtOnce("www.a.test")
}

// A reload that adds a host waits for that host's certificate. Without the
// wait every one of these first requests failed with "tls: internal error".
func TestAHostAddedByAReloadIsAnsweredAtOnce(t *testing.T) {
	up := upstream(t, "a")
	base := Site{Alias: "base.test", Hosts: []string{"base.test"}, Upstream: addr(up.Server)}
	h := startProxy(t, []Site{base})
	for i := range 20 {
		host := fmt.Sprintf("n%d.test", i)
		if err := h.p.Apply([]Site{base, {Alias: host, Hosts: []string{host}, Upstream: addr(up.Server)}}); err != nil {
			t.Fatal(err)
		}
		h.answersAtOnce(host)
	}
	// A host taken away and put back is waited for again.
	if err := h.p.Apply([]Site{base}); err != nil {
		t.Fatal(err)
	}
	if err := h.p.Apply([]Site{base, {Alias: "n0.test", Hosts: []string{"n0.test"}, Upstream: addr(up.Server)}}); err != nil {
		t.Fatal(err)
	}
	h.answersAtOnce("n0.test")
}

// recordAsked returns the hosts every load from here to the test's end
// asks for, in order.
func recordAsked(t *testing.T) *[]string {
	t.Helper()
	var asked []string
	certAsked = func(hosts []string) { asked = append(asked, hosts...) }
	t.Cleanup(func() { certAsked = nil })
	return &asked
}

// A pattern names no host to ask for, so a load does not wait for it; its
// names are issued at their first handshake.
func TestAPatternHostIsNotWaitedFor(t *testing.T) {
	asked := recordAsked(t)
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Hosts: []string{"a.test", "*.a.test"}, Upstream: addr(up.Server)}})
	if got := strings.Join(*asked, ","); got != "a.test" {
		t.Fatalf("the load asked for %q, want a.test alone", got)
	}
	h.getEventually(h.client("x.a.test", false), "x.a.test", "/hello")
}

// A host that has answered is not asked for again by the next load, and a
// proxy that takes Caddy over asks afresh.
func TestAHostIsAskedForOnceForEachOwner(t *testing.T) {
	asked := recordAsked(t)
	up := upstream(t, "a")
	site := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}
	other := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(up.Server)}
	h := startProxy(t, []Site{site})
	if err := h.p.Apply([]Site{site, other}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*asked, ","); got != "a.test,b.test" {
		t.Fatalf("one proxy's loads asked for %q, want a.test then b.test", got)
	}
	*asked = nil
	h2 := startProxyOpts(t, []Site{site}, Options{StorageRoot: h.storage})
	if got := strings.Join(*asked, ","); got != "a.test" {
		t.Fatalf("the proxy that took over asked for %q, want a.test again", got)
	}
	h2.answersAtOnce("a.test")
}

// The wait ends at its deadline for an address that never answers.
func TestAnswersForGivesUpAtItsDeadline(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close() // no TLS here
		}
	}()
	begin := time.Now()
	if answersFor(l.Addr().String(), "a.test", x509.NewCertPool(), begin.Add(200*time.Millisecond)) {
		t.Fatal("an address with no TLS answered")
	}
	if took := time.Since(begin); took < 200*time.Millisecond || took > 5*time.Second {
		t.Fatalf("gave up after %s, want about 200ms", took)
	}
}
