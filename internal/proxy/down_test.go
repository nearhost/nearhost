package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/nearhost/nearhost/internal/testutil"
)

// serveDown provisions a down page for d and sends it req, as Caddy's error
// routes do after a handler failed with status. It reports whether the page
// passed the request on, to the handler that writes the bare status.
func serveDown(t *testing.T, d *Down, req *http.Request, status int) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	if err := d.Provision(caddy.Context{Context: context.Background()}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	d.logger = zap.NewNop() // Provision's own, with no config, writes to stderr
	herr := caddyhttp.Error(status, errors.New("dial tcp 127.0.0.1:1: connect: connection refused"))
	req = req.WithContext(context.WithValue(req.Context(), caddyhttp.ErrorCtxKey, herr))
	w := httptest.NewRecorder()
	passed := false
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		passed = true
		w.WriteHeader(status)
		return nil
	})
	if err := d.ServeHTTP(w, req, next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w, passed
}

// navigation is a browser's request for a page.
func navigation(method string) *http.Request {
	r := httptest.NewRequest(method, "/admin?x=<i>", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	return r
}

func TestDownServesPageToBrowsers(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		w, passed := serveDown(t, &Down{Project: "shop", Port: 3000, Server: "mini"}, navigation("GET"), status)
		if passed || w.Code != status {
			t.Fatalf("status %d: code %d, passed on %v", status, w.Code, passed)
		}
		body := w.Body.String()
		for _, want := range []string{
			"<h1>shop is not answering</h1>",
			"Nearhost is running. Nothing answered on port 3000 on mini.",
			"Start the app, then reload.",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("status %d: body lacks %q:\n%s", status, want, body)
			}
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "admin") {
			t.Errorf("status %d: body has a script or the request path:\n%s", status, body)
		}
		hdr := w.Header()
		if hdr.Get("Content-Type") != "text/html; charset=utf-8" || hdr.Get("Cache-Control") != "no-store" ||
			hdr.Get("Content-Security-Policy") != homeCSP || hdr.Get("X-Content-Type-Options") != "nosniff" ||
			hdr.Get("Content-Length") != strconv.Itoa(len(body)) {
			t.Errorf("status %d: headers %v", status, hdr)
		}
	}
}

func TestDownNamesThisMachineForALocalProject(t *testing.T) {
	w, _ := serveDown(t, &Down{Project: "shop", Port: 5173}, navigation("GET"), 502)
	if want := "Nothing answered on port 5173 on this machine."; !strings.Contains(w.Body.String(), want) {
		t.Errorf("body lacks %q:\n%s", want, w.Body.String())
	}
}

func TestDownEscapesNames(t *testing.T) {
	d := &Down{Project: `<b>"a"&{http.request.uri}`, Port: 3000, Server: `<i>&{http.request.host}`}
	w, _ := serveDown(t, d, navigation("GET"), 502)
	body := w.Body.String()
	for _, want := range []string{
		"<h1>&lt;b&gt;&#34;a&#34;&amp;{http.request.uri} is not answering</h1>",
		"on port 3000 on &lt;i&gt;&amp;{http.request.host}.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<b>") || strings.Contains(body, "<i>") {
		t.Errorf("a name reached the page unescaped:\n%s", body)
	}
}

func TestDownAnswersHeadWithoutBody(t *testing.T) {
	w, passed := serveDown(t, &Down{Project: "shop", Port: 3000}, navigation("HEAD"), 502)
	if passed || w.Code != 502 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: code %d, passed on %v, body %q, headers %v", w.Code, passed, w.Body.String(), w.Header())
	}
}

func TestDownLeavesOtherRequestsBare(t *testing.T) {
	json := httptest.NewRequest("GET", "/api", nil)
	json.Header.Set("Accept", "application/json")
	noAccept := httptest.NewRequest("GET", "/", nil)
	post := navigation("POST")
	ws := navigation("GET")
	ws.Header.Set("Connection", "Upgrade")
	ws.Header.Set("Upgrade", "websocket")
	cases := map[string]struct {
		req    *http.Request
		status int
	}{
		"json":         {json, 502},
		"no accept":    {noAccept, 502},
		"post":         {post, 502},
		"upgrade":      {ws, 502},
		"other status": {navigation("GET"), 500},
	}
	for name, c := range cases {
		w, passed := serveDown(t, &Down{Project: "shop", Port: 3000}, c.req, c.status)
		if !passed || w.Code != c.status || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
			t.Errorf("%s: code %d, passed on %v, body %q, headers %v", name, w.Code, passed, w.Body.String(), w.Header())
		}
	}
}

// TestDownPageEndToEnd runs the proxy with one site whose app does not
// listen and one whose app answers 502 itself: a browser asking the first
// gets Nearhost's page, anything else the bare status, and the second's own
// answer passes through.
func TestDownPageEndToEnd(t *testing.T) {
	dead := testutil.FreePort(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "the app's own 502")
	}))
	t.Cleanup(failing.Close)
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	h := startProxyWith(t, []Site{
		{Alias: "dead.test", Hosts: []string{"dead.test"}, Upstream: "127.0.0.1:" + strconv.Itoa(dead), Project: "<dead>", Port: 3000, Server: "mini"},
		{Alias: "failing.test", Hosts: []string{"failing.test"}, Upstream: addr(failing), Project: "failing"},
	}, true)
	h.waitCerts(t, "dead.test", "failing.test")

	get := func(alias, path, accept string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest("GET", h.url(alias, path), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", accept)
		resp, err := h.client(alias, false).Do(req)
		if err != nil {
			t.Fatalf("GET %s%s: %v", alias, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := get("dead.test", "/page", "text/html,*/*;q=0.8")
	if resp.StatusCode != 502 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(body, "&lt;dead&gt; is not answering") || !strings.Contains(body, "on port 3000 on mini.") {
		t.Fatalf("browser: %d %v\n%s", resp.StatusCode, resp.Header, body)
	}
	if resp.Header.Get("Content-Security-Policy") != homeCSP || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("browser headers: %v", resp.Header)
	}

	resp, body = get("dead.test", "/api", "application/json")
	if resp.StatusCode != 502 || body != "" {
		t.Errorf("json: %d %q", resp.StatusCode, body)
	}

	resp, body = get("failing.test", "/", "text/html")
	if resp.StatusCode != 502 || body != "the app's own 502" {
		t.Errorf("the app's own 502: %d %q", resp.StatusCode, body)
	}

	// The failure stays in the request log and in Caddy's own log.
	waitUntil(t, 5*time.Second, func() bool { return h.rec.hasFor("dead.test", `"status":502`) }, "access log line for the 502")
	refused := refusedText(t, dead)
	waitUntil(t, 5*time.Second, func() bool { return h.rec.hasFor("", refused) }, "error log line for the 502")
	if e, ok := Captured(idOf(t, h, "dead.test", "/page")); !ok || e.Err == "" {
		t.Errorf("the failed exchange was not kept with its error: %+v", e)
	}
}

// refusedText is how this system words a refused connection, as the dial
// to port, on which nothing listens, reports it: "connection refused" on
// Unix, and "No connection could be made because the target machine
// actively refused it." on Windows.
func refusedText(t *testing.T, port int) string {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err == nil {
		c.Close()
		t.Fatalf("a dial to port %d, where nothing listens, connected", port)
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		t.Fatalf("a dial to port %d failed without an errno: %v", port, err)
	}
	return errno.Error()
}

// An error on a host with no site still has its line at error level, as
// Caddy logs it for a server with no error routes, and gets the bare
// status.
func TestDownLogOnlyLogsAndLeavesStatusBare(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	d := &Down{LogOnly: true}
	if err := d.Provision(caddy.Context{Context: context.Background()}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	d.logger = zap.New(core)
	for _, status := range []int{502, 500, 404} {
		herr := caddyhttp.Error(status, errors.New("it broke"))
		req := navigation("GET")
		req = req.WithContext(context.WithValue(req.Context(), caddyhttp.ErrorCtxKey, herr))
		w := httptest.NewRecorder()
		passed := false
		next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
			passed = true
			w.WriteHeader(status)
			return nil
		})
		if err := d.ServeHTTP(w, req, next); err != nil {
			t.Fatalf("ServeHTTP: %v", err)
		}
		if !passed || w.Code != status || w.Body.Len() != 0 {
			t.Errorf("status %d: code %d, passed on %v, body %q", status, w.Code, passed, w.Body.String())
		}
		want := zapcore.ErrorLevel
		if status < 500 {
			want = zapcore.DebugLevel
		}
		got := logs.TakeAll()
		if len(got) != 1 || got[0].Level != want || got[0].Message != "it broke" || got[0].ContextMap()["status"] != int64(status) {
			t.Errorf("status %d: logged %+v", status, got)
		}
	}
}

// serveGate provisions g and sends it req, reporting whether it passed the
// request on to the reverse proxy.
func serveGate(t *testing.T, g *Gate, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	if err := g.Provision(caddy.Context{Context: context.Background()}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	g.logger = zap.NewNop()
	w := httptest.NewRecorder()
	passed := false
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		passed = true
		return nil
	})
	if err := g.ServeHTTP(w, req, next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w, passed
}

// A shut gate answers itself, without calling the reverse proxy: a browser
// gets the page saying Nearhost is reconnecting, anything else the bare 502.
func TestGateAnswersWhileTheForwardIsDown(t *testing.T) {
	const up = "127.0.0.1:1"
	t.Cleanup(func() { ForgetForward(up) })
	g := &Gate{Upstream: up, Project: "shop", Server: "mini"}
	w, passed := serveGate(t, g, navigation("GET"))
	body := w.Body.String()
	if passed || w.Code != 502 {
		t.Fatalf("code %d, passed on %v", w.Code, passed)
	}
	for _, want := range []string{
		"<h1>shop is not answering</h1>",
		"<p>Nearhost is reconnecting to mini.</p>",
		"<p>Reload once it is back.</p>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Nothing answered") || strings.Contains(body, "Start the app") || strings.Contains(body, "<script") {
		t.Errorf("body has the other variant's copy or a script:\n%s", body)
	}
	if hdr := w.Header(); hdr.Get("Content-Security-Policy") != homeCSP || hdr.Get("Cache-Control") != "no-store" ||
		hdr.Get("Content-Length") != strconv.Itoa(len(body)) || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", hdr)
	}

	json := httptest.NewRequest("GET", "/api", nil)
	json.Header.Set("Accept", "application/json")
	ws := navigation("GET")
	ws.Header.Set("Upgrade", "websocket")
	for name, req := range map[string]*http.Request{"json": json, "post": navigation("POST"), "upgrade": ws} {
		w, passed := serveGate(t, g, req)
		if passed || w.Code != 502 || w.Body.Len() != 0 {
			t.Errorf("%s: code %d, passed on %v, body %q", name, w.Code, passed, w.Body.String())
		}
	}

	SetForwardUp(up, true)
	if w, passed := serveGate(t, g, navigation("GET")); !passed || w.Body.Len() != 0 {
		t.Errorf("forward up: passed on %v, body %q", passed, w.Body.String())
	}
}

// TestGateEndToEnd runs the proxy with two sites of one forward that is
// down: both answer 502 without dialling the app, a browser getting the
// reconnecting page, and the down period is logged once. A reload for
// another reason keeps them refusing. Once the forward is said to be up,
// the sites proxy to it at once, with no reload; said down again, they stop.
// A forward that is up to an app that does not answer gets A13's page.
func TestGateEndToEnd(t *testing.T) {
	var dialled atomic.Int32
	app := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "the app")
	}))
	app.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dialled.Add(1)
		}
	}
	app.Start()
	t.Cleanup(app.Close)
	up := addr(app)
	dead := "127.0.0.1:" + strconv.Itoa(testutil.FreePort(t))
	t.Cleanup(func() { ForgetForward(up); ForgetForward(dead) })
	sites := []Site{
		{Alias: "gated.test", Hosts: []string{"gated.test"}, Upstream: up, Forwarded: true, Project: "gated", Port: 80, Server: "mini"},
		{Alias: "other.test", Hosts: []string{"other.test"}, Upstream: up, Forwarded: true, Project: "other", Port: 80, Server: "mini"},
		{Alias: "dead.test", Hosts: []string{"dead.test"}, Upstream: dead, Forwarded: true, Project: "dead", Port: 3000, Server: "mini"},
	}
	h := startProxyWith(t, sites, false)
	h.waitCerts(t, "gated.test", "other.test", "dead.test")

	get := func(alias, accept string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("GET", h.url(alias, "/"), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", accept)
		resp, err := h.client(alias, false).Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", alias, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	refuses := func(when string) {
		t.Helper()
		for _, alias := range []string{"gated.test", "other.test"} {
			if code, body := get(alias, "text/html"); code != 502 || !strings.Contains(body, "Nearhost is reconnecting to mini.") {
				t.Errorf("%s, %s, browser: %d\n%s", when, alias, code, body)
			}
			if code, body := get(alias, "application/json"); code != 502 || body != "" {
				t.Errorf("%s, %s, json: %d %q", when, alias, code, body)
			}
		}
		if n := dialled.Load(); n != 0 {
			t.Fatalf("%s: the app got %d connections while its forward was down", when, n)
		}
	}
	refuses("down")
	// Four refused requests on two sites of one forward: one log line.
	waitUntil(t, 5*time.Second, func() bool { return h.rec.hasFor("", forwardDownMsg) }, "the down period's log line")
	if n := h.rec.countFor("", forwardDownMsg); n != 1 {
		t.Errorf("the down period was logged %d times, want once", n)
	}

	// A reload for another reason, such as a project starting.
	local := Site{Alias: "local.test", Hosts: []string{"local.test"}, Upstream: up}
	if err := h.p.Apply(append(slices.Clone(sites), local)); err != nil {
		t.Fatal(err)
	}
	refuses("after a reload")

	if !SetForwardUp(up, true) || SetForwardUp(up, true) {
		t.Error("SetForwardUp did not report the change once")
	}
	if code, body := get("gated.test", "text/html"); code != 200 || body != "the app" {
		t.Errorf("forward up: %d %q", code, body)
	}
	SetForwardUp(up, false)
	before := dialled.Load()
	if code, _ := get("gated.test", "text/html"); code != 502 {
		t.Errorf("forward down again: %d", code)
	}
	if n := dialled.Load(); n != before {
		t.Errorf("the app got %d new connections once its forward was down again", n-before)
	}
	// A new down period has its own line.
	waitUntil(t, 5*time.Second, func() bool { return h.rec.countFor("", forwardDownMsg) == 2 }, "the second down period's log line")

	// Up, to an app that does not answer: A13's page, from the error route.
	SetForwardUp(dead, true)
	if code, body := get("dead.test", "text/html"); code != 502 || !strings.Contains(body, "Nothing answered on port 3000 on mini.") {
		t.Errorf("forward up, app down: %d\n%s", code, body)
	}
}

// countFor counts the lines filed under alias that contain sub.
func (r *recorder) countFor(alias, sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if strings.HasPrefix(l, alias+" ") && strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// The down page keeps each status and its heading, and now carries the
// message's code.
func TestDownPageKeepsStatusAndCopy(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		w, _ := serveDown(t, &Down{Project: "shop", Port: 3000, Server: "mini"}, navigation("GET"), status)
		body := w.Body.String()
		if w.Code != status || !strings.Contains(body, "<h1>shop is not answering</h1>") ||
			!strings.Contains(body, `data-code="proxy.app-not-answering"`) || !strings.Contains(body, "message--error") {
			t.Errorf("status %d: %d\n%s", status, w.Code, body)
		}
	}
}
