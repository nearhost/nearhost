package proxy

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

// appScript is the inline script of the test app's index.html, between its
// tags exactly as the browser hashes it.
const appScript = "\n\t\t\tconsole.log(\"boot\");\n\t\t"

const appIndex = `<!doctype html>
<html><head><title>app</title>
<script src="/_app/immutable/x.js" type="module"></script>
</head><body><div><script>` + appScript + `</script></div></body></html>
`

// testApp is a built app as the pwa module holds one.
func testApp() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(appIndex)},
		"manifest.webmanifest": {Data: []byte(`{"name":"app"}`)},
		"_app/immutable/x.js":  {Data: []byte("export {}")},
		"_app/version.json":    {Data: []byte(`{"version":"1"}`)},
		"icons/icon-192.png":   {Data: []byte("png")},
	}
}

// setTestApp sets the test app for the test.
func setTestApp(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetApp(nil) })
	SetApp(testApp())
}

// wantAppCSP is the policy the app's index.html must be sent with, its
// hash computed here rather than by the code under test.
func wantAppCSP() string {
	sum := sha256.Sum256([]byte(appScript))
	return "default-src 'none'; script-src 'self' 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

func TestAppServesIndexForItsPages(t *testing.T) {
	setTestApp(t)
	freshSecret(t)
	for _, r := range []*http.Request{
		pageRequest("GET", "/"),
		pageRequest("GET", "/approve/abc"),
		pageRequest("GET", "/projects/shop?x=1"),
		pageRequest("GET", "/index.html"),
		pageRequest("GET", "/devices"),
		localhostRequest("GET", "/"),
		netRequest("GET", "/projects/shop", nil),
	} {
		var w *httptest.ResponseRecorder
		if r.Host == netPageHost+":7443" {
			w = serveNet(t, netHome(), r)
		} else {
			w = serveHomeRequest(t, r)
		}
		what := r.Host + r.URL.String()
		if w.Code != http.StatusOK || w.Body.String() != appIndex {
			t.Errorf("GET %s: %d %q, want the app's index.html", what, w.Code, w.Body.String())
			continue
		}
		hdr := w.Header()
		if got := hdr.Get("Content-Security-Policy"); got != wantAppCSP() {
			t.Errorf("GET %s: CSP\n %q\nwant\n %q", what, got, wantAppCSP())
		}
		if got := hdr.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET %s: Content-Type %q", what, got)
		}
		if got := hdr.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q, want no-cache", what, got)
		}
		if hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: no nosniff", what)
		}
	}
	if w := serveHomeRequest(t, pageRequest("HEAD", "/approve/abc")); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD /approve/abc: %d with %d bytes, want 200 and none", w.Code, w.Body.Len())
	}
}

func TestAppServesItsFiles(t *testing.T) {
	setTestApp(t)
	w := serveHomeRequest(t, pageRequest("GET", "/_app/immutable/x.js"))
	if w.Code != http.StatusOK || w.Body.String() != "export {}" {
		t.Fatalf("GET x.js: %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("x.js Cache-Control %q", got)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("x.js Content-Type %q, want JavaScript", got)
	}
	for path, ct := range map[string]string{
		"/manifest.webmanifest": "application/manifest+json",
		"/_app/version.json":    "application/json",
		"/icons/icon-192.png":   "image/png",
	} {
		w := serveHomeRequest(t, pageRequest("GET", path))
		want, _ := testApp()[strings.TrimPrefix(path, "/")]
		if w.Code != http.StatusOK || w.Body.String() != string(want.Data) {
			t.Errorf("GET %s: %d %q, want the app's file", path, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, ct) {
			t.Errorf("GET %s: Content-Type %q, want %s", path, got, ct)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q, want no-cache", path, got)
		}
	}
	// A missing build file is not found, never the index in its place.
	if w := serveHomeRequest(t, pageRequest("GET", "/_app/immutable/gone.js")); w.Code != http.StatusNotFound {
		t.Errorf("GET a missing chunk: %d, want 404", w.Code)
	}
	// The shell's files the app has none of are still today's.
	for _, path := range []string{"/sw.js", "/offline.html", "/icon-192.png"} {
		w := serveHomeRequest(t, pageRequest("GET", path))
		if w.Code != http.StatusOK || w.Body.String() == appIndex {
			t.Errorf("GET %s: %d, want the shell's file", path, w.Code)
		}
	}
}

func TestAppLeavesServerPathsAlone(t *testing.T) {
	setTestApp(t)
	secret := freshSecret(t)
	api := setFakeAPI(t)
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("doc"))
	}), nil)

	r := pageRequest("GET", "/api/v1/state")
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	if w := serveHomeRequest(t, r); w.Code != http.StatusOK || w.Body.String() != `"api"` {
		t.Errorf("GET /api/v1/state with the cookie: %d %q, want the API", w.Code, w.Body.String())
	}
	if got := api.called(); len(got) != 1 || got[0] != "GET /api/v1/state" {
		t.Errorf("the API was sent %v", got)
	}
	if w := serveHomeRequest(t, pageRequest("GET", "/api/v1/state")); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/state without the cookie: %d, want 401", w.Code)
	}
	if w := serveHomeRequest(t, pageRequest("GET", "/d/x/y.md")); w.Code != http.StatusOK || w.Body.String() != "doc" {
		t.Errorf("GET /d/x/y.md: %d %q, want the document", w.Code, w.Body.String())
	}
	// Not networked, pairing's paths and the server's own are not found.
	for _, path := range []string{"/api", "/pair", "/pair/abc", "/pair.js", "/auth", "/.well-known/x", "/.nearhost/x", "/profile.mobileconfig"} {
		w := serveHomeRequest(t, pageRequest("GET", path))
		if w.Body.String() == appIndex {
			t.Errorf("GET %s: answered with the app", path)
		}
		if path != "/api" && w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, w.Code)
		}
	}

	// Networked, pairing's paths reach pairing.
	setFakeDevices(t)
	for _, path := range []string{"/pair", "/pair/ABCD2345", "/auth?to=https%3A%2F%2Fapp.lab.example.dev%2F"} {
		w := serveNet(t, netHome(), netRequest("GET", path, nil))
		if w.Body.String() == appIndex {
			t.Errorf("networked GET %s: answered with the app", path)
		}
	}
}

func TestAppTakesGetAndHeadOnly(t *testing.T) {
	setTestApp(t)
	for _, path := range []string{"/anything", "/", "/_app/immutable/x.js"} {
		w := serveHomeRequest(t, pageRequest("POST", path))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("POST %s: %d Allow %q, want 405 GET, HEAD", path, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestAppKeepsTheKeyRedirect(t *testing.T) {
	setTestApp(t)
	secret := freshSecret(t)
	r := pageRequest("GET", "/?"+url.Values{"k": {secret}, "next": {"/approve/abc"}}.Encode())
	w := serveHomeRequest(t, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/approve/abc" {
		t.Errorf("/?k=: %d Location %q, want 303 /approve/abc", w.Code, w.Header().Get("Location"))
	}
	if got := w.Header().Get("Set-Cookie"); got != pageCookieHeader(secret) {
		t.Errorf("/?k=: Set-Cookie %q", got)
	}
}

func TestAppWinsOverTheConsole(t *testing.T) {
	setTestApp(t)
	secret := freshSecret(t)
	con := setFakeConsole(t)
	r := pageRequest("GET", "/")
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	if w := serveHomeRequest(t, r); w.Body.String() != appIndex {
		t.Errorf("GET / with the cookie and a console: %q, want the app", w.Body.String())
	}
	if got := con.called(); len(got) != 0 {
		t.Errorf("the console was sent %v", got)
	}
}

func TestAppUnsetServesTodaysPage(t *testing.T) {
	SetApp(testApp())
	SetApp(nil)
	w := serveHome(t, "1.2.3", "GET", "/")
	if w.Code != http.StatusOK || w.Body.String() == appIndex || w.Header().Get("Content-Security-Policy") != homeCSP {
		t.Errorf("GET / with no app: %d CSP %q, want today's page", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	if w := serveHome(t, "1.2.3", "GET", "/approve/abc"); w.Code != http.StatusNotFound {
		t.Errorf("GET /approve/abc with no app or console: %d, want 404", w.Code)
	}
	if w := serveHome(t, "1.2.3", "GET", "/manifest.webmanifest"); !strings.Contains(w.Body.String(), "Nearhost") {
		t.Errorf("GET /manifest.webmanifest with no app: %q, want the shell's", w.Body.String())
	}
}

// An app without an index.html is no app.
func TestAppWithoutIndexIsNone(t *testing.T) {
	t.Cleanup(func() { SetApp(nil) })
	SetApp(fstest.MapFS{".gitkeep": {}})
	if w := serveHome(t, "1.2.3", "GET", "/"); w.Header().Get("Content-Security-Policy") != homeCSP {
		t.Errorf("GET / with an unbuilt app: CSP %q, want today's page", w.Header().Get("Content-Security-Policy"))
	}
}

// appScriptHashes reads every inline script and only those.
func TestAppCSPHashesInlineScriptsOnly(t *testing.T) {
	two := `<script src="/a.js"></script><script>a()</script><SCRIPT type="module">b()</SCRIPT>`
	got := appCSP([]byte(two))
	for _, s := range []string{"a()", "b()"} {
		sum := sha256.Sum256([]byte(s))
		if !strings.Contains(got, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
			t.Errorf("CSP %q has no hash of %q", got, s)
		}
	}
	if strings.Count(got, "sha256-") != 2 {
		t.Errorf("CSP %q, want two hashes", got)
	}
}
