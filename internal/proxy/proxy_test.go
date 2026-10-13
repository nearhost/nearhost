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
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestMain points every directory Caddy might touch (instance.uuid lives under
// its app data dir, resolved from XDG_DATA_HOME at call time) at a throwaway
// location so tests never write into the real user's directories.
func TestMain(m *testing.M) {
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

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
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
	h := &harness{t: t, rec: &recorder{}, storage: t.TempDir(), httpsPort: freePort(t), httpPort: freePort(t)}
	SetSink(h.rec)
	t.Cleanup(func() { SetSink(nil) })
	h.p = New(Options{StorageRoot: h.storage, HTTPSPort: h.httpsPort, HTTPPort: h.httpPort})
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
