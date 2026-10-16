package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// serveHome provisions a home page for version and sends it one request.
func serveHome(t *testing.T, version, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	h := &Home{Version: version}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	w := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Error("the home page passed the request on")
		return nil
	})
	if err := h.ServeHTTP(w, httptest.NewRequest(method, path, nil), next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w
}

func TestHomeEscapesVersion(t *testing.T) {
	w := serveHome(t, "<b>", "GET", "/")
	body := w.Body.String()
	if !strings.Contains(body, "Nearhost &lt;b&gt;") {
		t.Errorf("body does not hold the escaped version:\n%s", body)
	}
	if strings.Contains(body, "<b>") {
		t.Error("the version reached the page unescaped")
	}
}

func TestHomeServesPage(t *testing.T) {
	w := serveHome(t, "1.2.3", "GET", "/")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, Content-Type %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{"<title>Nearhost</title>", "Run it anywhere.", "https://nearhost.dev", "https://nearhost.dev/docs/", "https://github.com/nearhost/nearhost", "Nearhost 1.2.3"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("page does not hold %q", want)
		}
	}
	// The page asks for nothing from elsewhere: no scripts, and a policy
	// that would block anything fetched.
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
	if strings.Contains(w.Body.String(), "<script") {
		t.Error("page has a script")
	}
}

func TestHomeDefaultsVersionToDev(t *testing.T) {
	if w := serveHome(t, "", "GET", "/"); !strings.Contains(w.Body.String(), "Nearhost dev") {
		t.Error("an empty version is not shown as dev")
	}
}

func TestHomeAnswersHeadWithoutBody(t *testing.T) {
	w := serveHome(t, "1.2.3", "HEAD", "/")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: status %d, %d body bytes, Content-Length %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}
}

func TestHomeRefusesOtherPathsAndMethods(t *testing.T) {
	if w := serveHome(t, "1.2.3", "GET", "/nope"); w.Code != 404 {
		t.Errorf("GET /nope: status %d, want 404", w.Code)
	}
	w := serveHome(t, "1.2.3", "POST", "/")
	if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST /: status %d, Allow %q, want 405 allowing GET, HEAD", w.Code, w.Header().Get("Allow"))
	}
}
