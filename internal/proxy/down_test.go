package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

// An error on a host with no site, such as one the home page returns, still
// has its line at error level, as Caddy logs it for a server with no error
// routes, and gets the bare status.
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

// A request a gate failed, because the site's forward is down, gets the
// page that says Nearhost is reconnecting, with the same headers.
func TestDownSaysReconnectingWhenTheForwardIsDown(t *testing.T) {
	d := &Down{Project: "shop", Port: 3000, Server: "mini"}
	if err := d.Provision(caddy.Context{Context: context.Background()}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	d.logger = zap.NewNop()
	req := navigation("GET")
	req = req.WithContext(context.WithValue(req.Context(), caddyhttp.ErrorCtxKey, caddyhttp.Error(502, errForwardDown)))
	w := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Error("the page passed the request on")
		return nil
	})
	if err := d.ServeHTTP(w, req, next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	body := w.Body.String()
	if w.Code != 502 {
		t.Errorf("code %d", w.Code)
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
		hdr.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("headers %v", hdr)
	}
}

// TestGateEndToEnd runs the proxy with a forwarded site whose forward is
// down and a site that is Down: both answer 502 without dialling the
// listener on their port, a browser getting the reconnecting page. Once the
// forward is said to be up, the forwarded site proxies to it at once, with
// no reload; said down again, it stops.
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
	t.Cleanup(func() { SetForwardUp(up, false) })
	h := startProxyWith(t, []Site{
		{Alias: "gated.test", Hosts: []string{"gated.test"}, Upstream: up, Forwarded: true, Project: "gated", Port: 80, Server: "mini"},
		{Alias: "down.test", Hosts: []string{"down.test"}, Down: true, Project: "down", Port: 80, Server: "mini"},
	}, false)
	h.waitCerts(t, "gated.test", "down.test")

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
	for _, alias := range []string{"gated.test", "down.test"} {
		if code, body := get(alias, "text/html"); code != 502 || !strings.Contains(body, "Nearhost is reconnecting to mini.") {
			t.Errorf("%s, browser: %d\n%s", alias, code, body)
		}
		if code, body := get(alias, "application/json"); code != 502 || body != "" {
			t.Errorf("%s, json: %d %q", alias, code, body)
		}
	}
	if n := dialled.Load(); n != 0 {
		t.Fatalf("the app got %d connections while its forward was down", n)
	}

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
}
