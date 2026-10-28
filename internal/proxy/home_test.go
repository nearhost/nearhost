package proxy

import (
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestHomeListsTrees(t *testing.T) {
	t.Cleanup(func() { SetDocuments(nil, nil) })
	plain := serveHome(t, "1.2.3", "GET", "/")

	SetDocuments(http.NotFoundHandler(), func() []HomeTree {
		return []HomeTree{{Server: "apollo", Root: "/srv/app", Docs: []HomeDoc{
			{Path: "docs/plan.md", URL: "https://localhost:6327/d/abc/docs/plan.md"},
			{Path: "notes.html", URL: "https://localhost:6327/d/abc/notes.html"},
		}}}
	})
	w := serveHome(t, "1.2.3", "GET", "/")
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("GET /: status %d", w.Code)
	}
	last := -1
	for _, want := range []string{"apollo", "/srv/app", `href="https://localhost:6327/d/abc/docs/plan.md"`, "docs/plan.md", `href="https://localhost:6327/d/abc/notes.html"`, "notes.html"} {
		i := strings.Index(body, want)
		if i < 0 {
			t.Fatalf("page does not hold %q:\n%s", want, body)
		}
		if i < last {
			t.Errorf("%q is out of order", want)
		}
		last = i
	}
	if got, want := w.Header().Get("Content-Security-Policy"), plain.Header().Get("Content-Security-Policy"); got != want {
		t.Errorf("Content-Security-Policy = %q, want the home page's %q", got, want)
	}
	if w.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q for %d bytes", w.Header().Get("Content-Length"), len(body))
	}

	// With no trees the page is what it is without a list, with no empty
	// heading.
	SetDocuments(http.NotFoundHandler(), func() []HomeTree { return nil })
	if got := serveHome(t, "1.2.3", "GET", "/").Body.String(); got != plain.Body.String() {
		t.Errorf("an empty list changed the page:\n%s", got)
	}
}

func TestHomeListEscapes(t *testing.T) {
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.NotFoundHandler(), func() []HomeTree {
		return []HomeTree{{Server: "local", Root: "/tmp/<script>", Docs: []HomeDoc{{Path: "a.md", URL: "https://localhost/d/abc/a.md"}}}}
	})
	body := serveHome(t, "1.2.3", "GET", "/").Body.String()
	if strings.Contains(body, "<script>") {
		t.Error("the root reached the page unescaped")
	}
	if !strings.Contains(body, "/tmp/&lt;script&gt;") {
		t.Errorf("page does not hold the escaped root:\n%s", body)
	}
}
