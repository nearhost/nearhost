package proxy

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const (
	framePage = "https://nearhost.lab.example.dev:6327"
	frameCSP  = "default-src 'self'; frame-ancestors 'none'"
)

func frameRequest(dest, site, ref string) *http.Request {
	r := httptest.NewRequest("GET", "https://app.lab.example.dev/", nil)
	if dest != "" {
		r.Header.Set("Sec-Fetch-Dest", dest)
	}
	if site != "" {
		r.Header.Set("Sec-Fetch-Site", site)
	}
	if ref != "" {
		r.Header.Set("Referer", ref)
	}
	return r
}

// serveFrame runs r through a FrameRule in front of an upstream that
// answers with anti-framing headers.
func serveFrame(t *testing.T, r *http.Request, upstream http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rule := &FrameRule{Page: framePage}
	rec := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		upstream(w, r)
		return nil
	})
	if err := rule.ServeHTTP(rec, r, next); err != nil {
		t.Fatal(err)
	}
	return rec
}

func denyingUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", frameCSP)
	_, _ = w.Write([]byte("hi"))
}

func TestFrameRuleStripsForThePage(t *testing.T) {
	rec := serveFrame(t, frameRequest("iframe", "", framePage+"/"), denyingUpstream)
	if v := rec.Header().Get("X-Frame-Options"); v != "" {
		t.Errorf("X-Frame-Options = %q", v)
	}
	if v := rec.Header().Get("Content-Security-Policy"); v != "default-src 'self'" {
		t.Errorf("CSP = %q", v)
	}
}

func TestFrameRuleStripsSameOriginInFrame(t *testing.T) {
	rec := serveFrame(t, frameRequest("iframe", "same-origin", "https://app.lab.example.dev/login"), denyingUpstream)
	if v := rec.Header().Get("X-Frame-Options"); v != "" {
		t.Errorf("X-Frame-Options = %q", v)
	}
	if v := rec.Header().Get("Content-Security-Policy"); v != "default-src 'self'" {
		t.Errorf("CSP = %q", v)
	}
}

func TestFrameRuleKeepsHeadersForOthers(t *testing.T) {
	cases := map[string]*http.Request{
		"direct":          frameRequest("", "", ""),
		"document":        frameRequest("document", "same-site", framePage+"/"),
		"cross-site":      frameRequest("iframe", "cross-site", "https://evil.example/"),
		"other port":      frameRequest("iframe", "same-site", "https://nearhost.lab.example.dev:9999/"),
		"no referer":      frameRequest("iframe", "", ""),
		"same-origin doc": frameRequest("document", "same-origin", "https://app.lab.example.dev/"),
	}
	for name, r := range cases {
		rec := serveFrame(t, r, denyingUpstream)
		if v := rec.Header().Get("X-Frame-Options"); v != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q", name, v)
		}
		if v := rec.Header().Get("Content-Security-Policy"); v != frameCSP {
			t.Errorf("%s: CSP = %q", name, v)
		}
	}
}

func TestFrameRuleDropsEmptyCSP(t *testing.T) {
	rec := serveFrame(t, frameRequest("iframe", "", framePage+"/"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		_, _ = w.Write([]byte("hi"))
	})
	if _, ok := rec.Header()["Content-Security-Policy"]; ok {
		t.Errorf("CSP kept: %q", rec.Header().Get("Content-Security-Policy"))
	}
}

// Each Content-Security-Policy line is a policy of its own, and the browser
// enforces them all: each is rewritten alone, so a second line's directive
// is never folded into the first, where it would be ignored. A line left
// empty is dropped.
func TestFrameRuleRewritesEachCSPLine(t *testing.T) {
	rec := serveFrame(t, frameRequest("iframe", "", framePage+"/"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Security-Policy", "script-src 'self'; frame-ancestors 'none'")
		w.Header().Add("Content-Security-Policy", "frame-ancestors 'self'")
		w.Header().Add("Content-Security-Policy", "script-src 'none'; img-src *")
		_, _ = w.Write([]byte("hi"))
	})
	got := rec.Header().Values("Content-Security-Policy")
	want := []string{"script-src 'self'", "script-src 'none'; img-src *"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("CSP lines = %q, want %q", got, want)
	}
}

func TestFrameRuleMatchesDirectiveCaseInsensitively(t *testing.T) {
	rec := serveFrame(t, frameRequest("iframe", "", framePage+"/"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "Frame-Ancestors 'none'; frame-src 'self'; frame-ancestors-not 'x'")
		w.WriteHeader(http.StatusOK)
	})
	if v := rec.Header().Get("Content-Security-Policy"); v != "frame-src 'self'; frame-ancestors-not 'x'" {
		t.Errorf("CSP = %q", v)
	}
}

func TestFrameRuleKeepsOtherHeaders(t *testing.T) {
	rec := serveFrame(t, frameRequest("iframe", "", framePage+"/"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusFound)
	})
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d", rec.Code)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 2 {
		t.Errorf("Set-Cookie = %v", got)
	}
	if v := rec.Header().Get("Location"); v != "/next" {
		t.Errorf("Location = %q", v)
	}
	if v := rec.Header().Get("X-Frame-Options"); v != "" {
		t.Errorf("X-Frame-Options = %q", v)
	}
}

// A WebSocket upgrade must get through the wrapper: Hijack and Flush reach
// the real writer through Unwrap.
func TestFrameRuleKeepsUpgradesAndFlush(t *testing.T) {
	rule := &FrameRule{Page: framePage}
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("X-Frame-Options", "DENY")
		rc := http.NewResponseController(w)
		conn, rw, err := rc.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return nil
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		return nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = rule.ServeHTTP(w, r, next)
	}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Referer", framePage+"/")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("status = %d", resp.StatusCode)
	}

	// Flush through the wrapper reaches the recorder.
	rec := serveFrame(t, frameRequest("iframe", "", framePage+"/"), func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
	})
	if !rec.Flushed {
		t.Error("not flushed")
	}
}

func TestFrameHandlerOnNetworkedSitesOnly(t *testing.T) {
	cfg := netJSON(t, netSites, netOptions(t))
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost")
	page := "https://nearhost." + netDomain + ":7443"
	for _, s := range netSites {
		for _, h := range s.Hosts {
			for name, r := range map[string]map[string]any{
				"site": routeFor(t, dig(t, srv, "routes"), h),
				"down": routeFor(t, dig(t, srv, "errors", "routes"), h),
			} {
				names := handlerNames(t, r)
				if names[0] == tagModule {
					names = names[1:]
				}
				if len(names) < 2 || names[0] != deviceModule || names[1] != frameModule {
					t.Errorf("%s %s handlers = %v", name, h, names)
				}
				i := 1
				if handlerNames(t, r)[0] == tagModule {
					i = 2
				}
				if got := dig(t, r, "handle", i, "page"); got != page {
					t.Errorf("%s %s frame page = %v, want %s", name, h, got, page)
				}
			}
		}
	}

	plain := Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, Version: "1.2.3"}
	raw, err := BuildConfig(netSites, plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), frameModule) {
		t.Errorf("config without a network section has %s", frameModule)
	}
}
