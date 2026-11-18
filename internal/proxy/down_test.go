package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

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
	waitUntil(t, 5*time.Second, func() bool { return h.rec.hasFor("", "connection refused") }, "error log line for the 502")
	if e, ok := Captured(idOf(t, h, "dead.test", "/page")); !ok || e.Err == "" {
		t.Errorf("the failed exchange was not kept with its error: %+v", e)
	}
}
