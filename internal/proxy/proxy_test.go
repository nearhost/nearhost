package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"github.com/coder/websocket"

	"github.com/nearhost/nearhost/internal/testutil"
)

// TestMain points every directory Caddy might touch (instance.uuid lives under
// its app data dir, resolved from XDG_DATA_HOME at call time) at a throwaway
// location so tests never write into the real user's directories.
func TestMain(m *testing.M) {
	if helper := os.Getenv("NEARHOST_PROXY_HELPER"); helper != "" {
		os.Exit(runHelper(helper))
	}
	dir, err := os.MkdirTemp("", "nearhost-proxy-test-")
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

// freePort is a port for a proxy listener. It comes from testutil's block,
// below the ports the system hands out by itself, and is free on every
// interface for TCP and UDP, as Caddy binds it: a port free on 127.0.0.1
// alone can still be taken on the wildcard address.
func freePort(t *testing.T) int {
	t.Helper()
	return testutil.ProxyPort(t)
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) Log(alias, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, alias+" "+line)
}

// hasFor reports whether a line tagged with exactly alias contains sub.
func (r *recorder) hasFor(alias, sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.HasPrefix(l, alias+" ") && strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// seen is what the upstream recorded about the last /hello request.
type seen struct {
	mu                     sync.Mutex
	host, proto, forwarded string
}

func (s *seen) get() (string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.host, s.proto, s.forwarded
}

type upstreamSrv struct {
	*httptest.Server
	seen   *seen
	chunk2 chan struct{} // test closes/sends after reading chunk one
}

func upstream(t *testing.T, name string) *upstreamSrv {
	t.Helper()
	sn := &seen{}
	up := &upstreamSrv{seen: sn, chunk2: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		sn.mu.Lock()
		sn.host, sn.proto, sn.forwarded = r.Host, r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-For")
		sn.mu.Unlock()
		fmt.Fprintf(w, "hello from %s", name)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		w.Write(append([]byte("got "), b...))
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("one\n"))
		w.(http.Flusher).Flush()
		select {
		case <-up.chunk2: // only proceeds once the test has read chunk one
		case <-time.After(5 * time.Second):
			return
		}
		w.Write([]byte("two\n"))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if err := c.Write(ctx, typ, append([]byte("echo:"), data...)); err != nil {
				return
			}
		}
	})
	up.Server = httptest.NewServer(mux)
	t.Cleanup(up.Server.Close)
	return up
}

func addr(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

type harness struct {
	t         *testing.T
	p         *Proxy
	rec       *recorder
	storage   string
	httpsPort int
	httpPort  int
	pool      *x509.CertPool
}

// startProxy starts a real in-process Caddy on free high ports with a temp
// storage dir and a recording log sink. It trusts only that storage's root.
func startProxy(t *testing.T, sites []Site) *harness {
	t.Helper()
	return startProxyWith(t, sites, false)
}

// startProxyWith is startProxy, keeping exchanges when capture is set.
func startProxyWith(t *testing.T, sites []Site, capture bool) *harness {
	t.Helper()
	h := &harness{t: t, rec: &recorder{}, storage: t.TempDir(), httpsPort: freePort(t), httpPort: freePort(t)}
	SetSink(h.rec)
	t.Cleanup(func() { SetSink(nil) })
	h.p = New(Options{StorageRoot: h.storage, HTTPSPort: h.httpsPort, HTTPPort: h.httpPort, Capture: capture})
	if err := h.p.Apply(sites); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { h.p.Stop() })
	if !h.p.Running() {
		t.Fatal("expected Running after Apply")
	}
	var root *x509.Certificate
	waitUntil(t, 10*time.Second, func() bool {
		c, err := RootCert(h.storage)
		root = c
		return err == nil
	}, "root certificate")
	h.pool = x509.NewCertPool()
	h.pool.AddCert(root)
	return h
}

// client returns an HTTPS client that dials the proxy's loopback port while
// presenting alias as SNI and Host.
func (h *harness) client(alias string, keepAlive bool) *http.Client {
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: h.pool, ServerName: alias},
		DisableKeepAlives: !keepAlive,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", h.httpsPort))
		},
	}
	h.t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func (h *harness) url(alias, path string) string {
	return fmt.Sprintf("https://%s:%d%s", alias, h.httpsPort, path)
}

// getEventually retries a GET until it succeeds: Caddy issues the leaf
// certificate asynchronously after Load returns, so early handshakes fail.
func (h *harness) getEventually(c *http.Client, alias, path string) (*http.Response, string) {
	h.t.Helper()
	var resp *http.Response
	var body string
	waitUntil(h.t, 10*time.Second, func() bool {
		r, err := c.Get(h.url(alias, path))
		if err != nil {
			return false
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		resp, body = r, string(b)
		return true
	}, "HTTPS response for "+alias)
	return resp, body
}

func TestProxyRuntime(t *testing.T) {
	upA := upstream(t, "a")
	upB := upstream(t, "b")
	h := startProxy(t, []Site{{Alias: "a.test", Upstream: addr(upA.Server)}})
	client := h.client("a.test", true)

	// 1 + 5: body, forwarded headers and certificate chain.
	resp, body := h.getEventually(client, "a.test", "/hello")
	if resp.StatusCode != 200 || body != "hello from a" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	host, proto, fwd := upA.seen.get()
	if host != "a.test:"+fmt.Sprint(h.httpsPort) && host != "a.test" {
		t.Errorf("upstream Host = %q, want the alias", host)
	}
	if proto != "https" {
		t.Errorf("X-Forwarded-Proto = %q", proto)
	}
	if fwd == "" {
		t.Error("X-Forwarded-For is empty")
	}
	if resp.TLS == nil || len(resp.TLS.VerifiedChains) == 0 {
		t.Fatal("no verified certificate chain")
	}
	chain := resp.TLS.VerifiedChains[0]
	if rootCN := chain[len(chain)-1].Subject.CommonName; !strings.Contains(rootCN, CAName) {
		t.Errorf("chain root CN = %q, want it to contain %q", rootCN, CAName)
	}
	if chain[0].Subject.CommonName == chain[len(chain)-1].Subject.CommonName {
		t.Error("leaf should differ from root")
	}

	// 2: WebSocket echo, kept open across the Apply below (6).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, fmt.Sprintf("wss://a.test:%d/ws", h.httpsPort), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	defer ws.CloseNow()
	echo := func(msg string) {
		t.Helper()
		if err := ws.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatalf("ws write: %v", err)
		}
		_, data, err := ws.Read(ctx)
		if err != nil || string(data) != "echo:"+msg {
			t.Fatalf("websocket echo = %q, %v", data, err)
		}
	}
	echo("ping")

	// 3: streaming arrives incrementally. The upstream blocks on chunk2 until
	// the test has read chunk one, so buffering would deadlock until timeout.
	sresp, err := client.Get(h.url("a.test", "/stream"))
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(sresp.Body)
	line, err := br.ReadString('\n')
	if err != nil || line != "one\n" {
		t.Fatalf("first chunk = %q, %v", line, err)
	}
	close(upA.chunk2)
	line, err = br.ReadString('\n')
	if err != nil || line != "two\n" {
		t.Fatalf("second chunk = %q, %v", line, err)
	}
	sresp.Body.Close()

	// 4: plain HTTP redirects to https.
	plain := &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", h.httpPort))
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	redir, err := plain.Get(fmt.Sprintf("http://a.test:%d/hello", h.httpPort))
	if err != nil {
		t.Fatalf("GET http: %v", err)
	}
	redir.Body.Close()
	if loc := redir.Header.Get("Location"); redir.StatusCode/100 != 3 || !strings.HasPrefix(loc, "https://a.test") {
		t.Fatalf("expected redirect to https, got %d %q", redir.StatusCode, loc)
	}

	// 7: access log tagged with the right alias.
	waitUntil(t, 5*time.Second, func() bool { return h.rec.hasFor("a.test", "/hello") }, "access log line tagged a.test")
	if h.rec.hasFor("b.test", "/hello") {
		t.Error("a.test request logged under b.test")
	}

	// 6: Apply a changed site set: b appears, a goes away, the open socket lives.
	if err := h.p.Apply([]Site{{Alias: "b.test", Upstream: addr(upB.Server)}}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	bresp, bbody := h.getEventually(h.client("b.test", false), "b.test", "/hello")
	if bresp.StatusCode != 200 || bbody != "hello from b" {
		t.Fatalf("b.test status=%d body=%q", bresp.StatusCode, bbody)
	}
	waitUntil(t, 5*time.Second, func() bool { return h.rec.hasFor("b.test", "/hello") }, "access log line tagged b.test")
	gone := h.client("a.test", false)
	waitUntil(t, 10*time.Second, func() bool {
		r, err := gone.Get(h.url("a.test", "/hello"))
		if err != nil {
			return true
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return !strings.Contains(string(b), "hello from a")
	}, "a.test to stop being served")
	echo("after-apply")
}

func TestStopReleasesPortsAndIsIdempotent(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	if err := h.p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if h.p.Running() {
		t.Fatal("Running after Stop")
	}
	if err := h.p.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	for _, port := range []int{h.httpsPort, h.httpPort} {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			t.Fatalf("port %d not released: %v", port, err)
		}
		l.Close()
	}
}

func TestApplyAndStopAreSafeConcurrently(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, nil)
	sites := []Site{{Alias: "a.test", Upstream: addr(up.Server)}}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 4; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); errs <- h.p.Apply(sites) }()
		go func() { defer wg.Done(); errs <- h.p.Apply(nil) }()
		go func() { defer wg.Done(); errs <- h.p.Stop() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Apply or Stop: %v", err)
		}
	}
	// Settle on a known final state and let certificate issuance finish, so
	// nothing is still writing to storage when the test directory is removed.
	if err := h.p.Apply(sites); err != nil {
		t.Fatal(err)
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestSinkDoesNotLeakAcrossResets(t *testing.T) {
	rec := &recorder{}
	SetSink(rec)
	SetSink(nil)
	emit("x.test", "line")
	if rec.hasFor("x.test", "line") {
		t.Fatal("reset sink still received lines")
	}
}

func TestRootCertMissingReturnsError(t *testing.T) {
	if _, err := RootCert(t.TempDir()); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestStopOnlyAffectsTheOwningProxy(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}})
	other := New(Options{StorageRoot: t.TempDir(), HTTPSPort: h.httpsPort, HTTPPort: h.httpPort})
	if other.Running() {
		t.Fatal("a Proxy that never applied must not report Running")
	}
	if err := other.Stop(); err != nil {
		t.Fatal(err)
	}
	if !h.p.Running() {
		t.Fatal("Stop on another Proxy stopped the owner")
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	// A second Proxy that applies takes ownership; the first can no longer
	// stop it.
	if err := other.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if h.p.Running() || !other.Running() {
		t.Fatalf("ownership wrong: first=%v second=%v", h.p.Running(), other.Running())
	}
	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if !other.Running() {
		t.Fatal("stale Proxy.Stop shut down the current owner")
	}
	if err := other.Stop(); err != nil {
		t.Fatal(err)
	}
}

// Caddy resolves its app data dir (instance.uuid) from XDG_DATA_HOME at call
// time, so fresh dirs here prove the proxy never writes into them, whatever
// earlier tests in the process did.
func TestApplyIfRunningLeavesAnotherOwnersProxyAlone(t *testing.T) {
	up := upstream(t, "a")
	sites := []Site{{Alias: "a.test", Upstream: addr(up.Server)}}
	h := startProxy(t, sites)
	// The owner changes its own configuration as with Apply.
	if applied, err := h.p.ApplyIfRunning(sites); err != nil || !applied {
		t.Fatalf("ApplyIfRunning on the owner = %v, %v", applied, err)
	}
	// A Proxy that never applied owns nothing: it loads nothing.
	other := New(Options{StorageRoot: t.TempDir(), HTTPSPort: h.httpsPort, HTTPPort: h.httpPort})
	if applied, err := other.ApplyIfRunning(nil); err != nil || applied {
		t.Fatalf("ApplyIfRunning on a Proxy that never applied = %v, %v", applied, err)
	}
	if !h.p.Running() || other.Running() {
		t.Fatalf("ownership wrong: first=%v second=%v", h.p.Running(), other.Running())
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	// Once another Proxy has taken over, the first one cannot take Caddy
	// back this way, and the newer configuration stays.
	if err := other.Apply(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Stop() })
	if applied, err := h.p.ApplyIfRunning(sites); err != nil || applied {
		t.Fatalf("ApplyIfRunning on a replaced Proxy = %v, %v", applied, err)
	}
	if h.p.Running() || !other.Running() {
		t.Fatalf("ownership wrong after a stale ApplyIfRunning: first=%v second=%v", h.p.Running(), other.Running())
	}
	if _, err := h.client("a.test", false).Get(h.url("a.test", "/hello")); err == nil {
		t.Fatal("the replaced Proxy's site is served again")
	}
}

func TestApplyWritesNothingOutsideStorage(t *testing.T) {
	dataHome, configHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dataHome, configHome} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			t.Errorf("unexpected entry %q written under %s", e.Name(), dir)
		}
	}
}

// The proxy has to listen on every interface, so the routes themselves keep
// other machines out. Caddy's matcher compares the textual remote address and
// does not unmap IPv4-mapped IPv6 addresses, hence the third range.
func TestLoopbackMatcherAcceptsOnlyLoopbackClients(t *testing.T) {
	m := &caddyhttp.MatchRemoteIP{Ranges: loopbackRanges}
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()
	if err := m.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":          true,
		"127.8.9.10:5000":         true,
		"[::1]:5000":              true,
		"[::ffff:127.0.0.1]:5000": true,
		"[::ffff:127.9.9.9]:5000": true,
		"192.168.1.20:5000":       false,
		"10.0.0.1:5000":           false,
		"128.0.0.1:5000":          false,
		"[::ffff:10.0.0.1]:5000":  false,
		"[::2]:5000":              false,
		"[fe80::1%en0]:5000":      false,
		"[2001:db8::1]:5000":      false,
	} {
		got, err := m.MatchWithError(&http.Request{RemoteAddr: addr})
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if got != want {
			t.Errorf("client %s accepted = %v, want %v", addr, got, want)
		}
	}
}

type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

// The same ranges decide who gets a TLS handshake at all. This matcher reads
// the address of the connection itself.
func TestLoopbackHandshakeMatcherAcceptsOnlyLoopbackClients(t *testing.T) {
	m := &caddytls.MatchRemoteIP{Ranges: loopbackRanges}
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()
	if err := m.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.8.9.10", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"192.168.1.20", false},
		{"10.0.0.1", false},
		{"128.0.0.1", false},
		{"::ffff:10.0.0.1", false},
		{"::2", false},
		{"2001:db8::1", false},
	} {
		remote := &net.TCPAddr{IP: net.ParseIP(c.ip), Port: 5000}
		if got := m.Match(&tls.ClientHelloInfo{ServerName: "a.test", Conn: addrConn{remote: remote}}); got != c.want {
			t.Errorf("handshake from %s accepted = %v, want %v", remote, got, c.want)
		}
	}
	zoned := &net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 5000, Zone: "en0"}
	if m.Match(&tls.ClientHelloInfo{Conn: addrConn{remote: zoned}}) {
		t.Errorf("handshake from %s accepted", zoned)
	}
}

// nonLoopbackAddrs lists the addresses of this machine that are neither
// loopback nor link-local. A connection made to one of them from this machine
// has that address as its source too.
func nonLoopbackAddrs(t *testing.T) []net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []net.IP
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() || n.IP.IsLinkLocalMulticast() || n.IP.IsUnspecified() {
			continue
		}
		out = append(out, n.IP)
	}
	return out
}

func TestProxyRefusesClientsThatAreNotLoopback(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "secret dev page")
	}))
	t.Cleanup(up.Close)
	h := startProxy(t, []Site{{Alias: "a.test", Upstream: addr(up)}})

	// A loopback client is served, which also means the certificate exists.
	resp, body := h.getEventually(h.client("a.test", false), "a.test", "/")
	if resp.StatusCode != 200 || body != "secret dev page" {
		t.Fatalf("loopback client: status=%d body=%q", resp.StatusCode, body)
	}
	served := hits.Load()

	// request sends one GET for host over a fresh TLS connection to ip and
	// reports whether the proxy was reached, and what came back.
	request := func(ip net.IP, host string) (reached bool, status int, body string, err error) {
		d := &net.Dialer{Timeout: 5 * time.Second}
		raw, err := d.Dial("tcp", net.JoinHostPort(ip.String(), fmt.Sprint(h.httpsPort)))
		if err != nil {
			return false, 0, "", err
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(10 * time.Second))
		if got := raw.RemoteAddr().(*net.TCPAddr).IP; got.IsLoopback() {
			t.Fatalf("connection to %s arrived at loopback address %s", ip, got)
		}
		if got := raw.LocalAddr().(*net.TCPAddr).IP; got.IsLoopback() {
			t.Fatalf("connection to %s left from loopback address %s", ip, got)
		}
		conn := tls.Client(raw, &tls.Config{RootCAs: h.pool, ServerName: "a.test"})
		if err := conn.Handshake(); err != nil {
			return true, 0, "", err
		}
		if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host); err != nil {
			return true, 0, "", err
		}
		r, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return true, 0, "", err
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return true, r.StatusCode, string(b), nil
	}

	// handshake reports whether a TLS handshake for sni from ip went through,
	// and whether the proxy showed a certificate on the way.
	handshake := func(ip net.IP, sni string) (ok, sawCert bool) {
		raw, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("tcp", net.JoinHostPort(ip.String(), fmt.Sprint(h.httpsPort)))
		if err != nil {
			t.Errorf("%s: second connection: %v", ip, err)
			return false, false
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(10 * time.Second))
		conn := tls.Client(raw, &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true, // any certificate at all is what is looked for
			VerifyPeerCertificate: func(certs [][]byte, _ [][]*x509.Certificate) error {
				sawCert = sawCert || len(certs) > 0
				return nil
			},
		})
		return conn.Handshake() == nil, sawCert
	}

	tried := 0
	for _, ip := range nonLoopbackAddrs(t) {
		reached, status, body, err := request(ip, "a.test")
		if !reached {
			t.Logf("%s: not dialable, skipping it: %v", ip, err)
			continue
		}
		tried++
		// The refusal comes in the handshake, before any certificate: a
		// served name and an unknown one look the same from outside.
		for _, sni := range []string{"a.test", "nothing.test"} {
			if ok, sawCert := handshake(ip, sni); ok || sawCert {
				t.Errorf("client from %s asking for %s: handshake completed = %v, certificate shown = %v, want neither", ip, sni, ok, sawCert)
			}
		}
		if err == nil {
			t.Errorf("client from %s got an answer (status %d, body %q), want the connection dropped", ip, status, body)
		}
		// A name the proxy does not serve is dropped the same way, so the
		// answer does not tell which aliases exist.
		if _, status, body, err := request(ip, "nothing.test"); err == nil {
			t.Errorf("client from %s asking for an unknown host got status %d body %q, want the connection dropped", ip, status, body)
		}
	}
	if got := hits.Load(); got != served {
		t.Errorf("the upstream received %d request(s) from non-loopback clients", got-served)
	}

	// A loopback client asking for a host that is not served is dropped too.
	c := h.client("a.test", false)
	req, _ := http.NewRequest("GET", h.url("a.test", "/"), nil)
	req.Host = "nothing.test"
	if r, err := c.Do(req); err == nil {
		r.Body.Close()
		t.Errorf("unknown host from loopback got status %d, want the connection dropped", r.StatusCode)
	}
	// And the site is still served to loopback afterwards.
	if resp, body := h.getEventually(h.client("a.test", false), "a.test", "/"); resp.StatusCode != 200 || body != "secret dev page" {
		t.Errorf("loopback client after the refusals: status=%d body=%q", resp.StatusCode, body)
	}
	// So is a client on the IPv6 loopback address, when this machine has one.
	if raw, err := net.DialTimeout("tcp", net.JoinHostPort("::1", fmt.Sprint(h.httpsPort)), 5*time.Second); err != nil {
		t.Logf("::1 is not dialable, not checking the IPv6 loopback client: %v", err)
	} else {
		raw.Close()
		if ok, sawCert := handshake(net.IPv6loopback, "a.test"); !ok || !sawCert {
			t.Errorf("client from ::1: handshake completed = %v, certificate shown = %v, want both", ok, sawCert)
		}
	}
	// Only the non-loopback part could not run; the loopback checks above did.
	if tried == 0 {
		t.Skip("this machine has no reachable address besides loopback, so a non-loopback client cannot be simulated")
	}
}
