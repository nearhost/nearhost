package proxy

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/nearhost/nearhost/internal/testutil"
)

// freshSecret points the data directory at an empty one for the test and
// returns the page secret made there.
func freshSecret(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	secret, err := pageSecret()
	if err != nil {
		t.Fatalf("pageSecret: %v", err)
	}
	return secret
}

// wrongSecret is a secret of the right length that is not secret.
func wrongSecret(secret string) string {
	last := "0"
	if strings.HasSuffix(secret, "0") {
		last = "1"
	}
	return secret[:len(secret)-1] + last
}

// pageCookieHeader is the Set-Cookie header the page sends for secret.
func pageCookieHeader(secret string) string {
	return "__Host-nearhost=" + secret + "; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=31536000"
}

func TestPageSecretIsPrivateAndStable(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	first, err := pageSecret()
	if err != nil {
		t.Fatalf("pageSecret: %v", err)
	}
	if b, err := hex.DecodeString(first); err != nil || len(b) != 32 {
		t.Fatalf("secret is not 32 bytes of hex (%d chars, %v)", len(first), err)
	}
	path := filepath.Join(data, "nearhost", "page-secret")
	testutil.WantPerm(t, path, 0o600)
	if onDisk, err := os.ReadFile(path); err != nil || string(onDisk) != first {
		t.Fatalf("the file does not hold the secret returned (%v)", err)
	}
	second, err := pageSecret()
	if err != nil || second != first {
		t.Fatalf("a second call returned another secret (%v)", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the data directory holds %d entries, want only page-secret", len(entries))
	}
}

func TestPageSecretConcurrentFirstCallsAgree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const n = 16
	got := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			got[i], errs[i] = pageSecret()
		})
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if got[i] != got[0] {
			t.Fatalf("call %d returned another secret than call 0", i)
		}
	}
}

func TestPageSecretReplacesMalformedFile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	path := filepath.Join(data, "nearhost", "page-secret")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64)} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		secret, err := pageSecret()
		if err != nil {
			t.Fatalf("pageSecret over %q: %v", bad, err)
		}
		if onDisk, _ := os.ReadFile(path); string(onDisk) != secret || len(secret) != 64 {
			t.Fatalf("a malformed file %q was not replaced with the secret returned", bad)
		}
		testutil.WantPerm(t, path, 0o600)
	}
}

func TestPageKeyURL(t *testing.T) {
	secret := freshSecret(t)
	got, err := PageKeyURL("https://localhost:6327")
	if want := "https://localhost:6327/?k=" + secret; err != nil || got != want {
		t.Errorf("PageKeyURL = %q, %v; want %q", got, err, want)
	}
}

func TestSecretMatches(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	for _, c := range []struct {
		got  string
		want bool
	}{
		{secret, true},
		{wrongSecret(secret), false},
		{secret[:63], false},
		{secret + "0", false},
		{"", false},
	} {
		if secretMatches(c.got, secret) != c.want {
			t.Errorf("secretMatches(%q) = %v", c.got, !c.want)
		}
	}
	if secretMatches("", "") {
		t.Error("an empty secret matched")
	}
}

// serveHomeRequest provisions a home page and sends it r.
func serveHomeRequest(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h := &Home{Version: "1.2.3"}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	w := httptest.NewRecorder()
	if err := h.ServeHTTP(w, r, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w
}

// withCookie is a GET of path carrying the page cookie with value.
func withCookie(path, value string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: value})
	return r
}

// listTrees stores a documents list of one tree for the test.
func listTrees(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.NotFoundHandler(), func() []HomeTree {
		return []HomeTree{{Server: "apollo", Root: "/srv/app", Docs: []HomeDoc{
			{Path: "docs/plan.md", URL: "/d/abc123/docs/plan.md"},
		}}}
	})
}

// wantNoList fails when body shows anything of the stored list.
func wantNoList(t *testing.T, what, body, plain string) {
	t.Helper()
	for _, leak := range []string{"apollo", "/srv/app", "plan.md", "abc123", "/d/"} {
		if strings.Contains(body, leak) {
			t.Errorf("%s: the page shows %q", what, leak)
		}
	}
	if body != plain {
		t.Errorf("%s: the page differs from the one with no documents:\n%s", what, body)
	}
}

func TestHomeListsTreesOnlyWithTheCookie(t *testing.T) {
	secret := freshSecret(t)
	plain := serveHome(t, "1.2.3", "GET", "/").Body.String()
	listTrees(t)

	wantNoList(t, "no cookie", serveHome(t, "1.2.3", "GET", "/").Body.String(), plain)
	wantNoList(t, "a wrong cookie", serveHomeRequest(t, withCookie("/", wrongSecret(secret))).Body.String(), plain)
	wantNoList(t, "an empty cookie", serveHomeRequest(t, withCookie("/", "")).Body.String(), plain)
	short := serveHomeRequest(t, withCookie("/", secret[:32])).Body.String()
	wantNoList(t, "half the secret", short, plain)

	w := serveHomeRequest(t, withCookie("/", secret))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `<a href="/d/abc123/docs/plan.md">docs/plan.md</a>`) || !strings.Contains(body, "apollo") {
		t.Fatalf("with the cookie: status %d, page without the list:\n%s", w.Code, body)
	}
	if strings.Contains(body, secret) {
		t.Error("the page holds the secret")
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("the page sets a cookie on a plain request")
	}
	if !strings.Contains(w.Header().Get("Vary"), "Cookie") {
		t.Errorf("Vary = %q, want Cookie", w.Header().Get("Vary"))
	}

	// HEAD is the only other way to read /, and it has no body.
	r := withCookie("/", wrongSecret(secret))
	r.Method = "HEAD"
	if w := serveHomeRequest(t, r); w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(len(plain)) {
		t.Errorf("HEAD with a wrong cookie: %d body bytes, Content-Length %q, want the plain page's %d", w.Body.Len(), w.Header().Get("Content-Length"), len(plain))
	}
}

func TestHomeKeySetsCookieAndRedirects(t *testing.T) {
	secret := freshSecret(t)
	listTrees(t)
	for _, method := range []string{"GET", "HEAD"} {
		w := serveHomeRequest(t, httptest.NewRequest(method, "/?k="+secret, nil))
		hdr := w.Header()
		if w.Code != 303 || hdr.Get("Location") != "/" {
			t.Fatalf("%s /?k=right: status %d, Location %q, want 303 to /", method, w.Code, hdr.Get("Location"))
		}
		if got := hdr.Values("Set-Cookie"); len(got) != 1 || got[0] != pageCookieHeader(secret) {
			t.Errorf("%s /?k=right: Set-Cookie %d values, want exactly the page cookie", method, len(got))
		}
		if hdr.Get("Referrer-Policy") != "no-referrer" || hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("%s /?k=right: Referrer-Policy %q, Cache-Control %q", method, hdr.Get("Referrer-Policy"), hdr.Get("Cache-Control"))
		}
		if body := w.Body.String(); strings.Contains(body, secret) || strings.Contains(body, "apollo") {
			t.Errorf("%s /?k=right: the redirect's body shows the secret or the list", method)
		}
	}
}

func TestHomeWrongKeySetsNothing(t *testing.T) {
	secret := freshSecret(t)
	listTrees(t)
	// The cases are named by number, so a failure prints no part of the
	// secret.
	for i, k := range []string{wrongSecret(secret), "", secret[:10], secret + "00", "%00"} {
		w := serveHomeRequest(t, httptest.NewRequest("GET", "/?k="+k, nil))
		hdr := w.Header()
		if w.Code != 303 || hdr.Get("Location") != "/" {
			t.Errorf("wrong key %d: status %d, Location %q, want 303 to /", i, w.Code, hdr.Get("Location"))
		}
		if len(hdr.Values("Set-Cookie")) != 0 {
			t.Errorf("wrong key %d set a cookie", i)
		}
		if hdr.Get("Referrer-Policy") != "no-referrer" || hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("wrong key %d: Referrer-Policy %q, Cache-Control %q", i, hdr.Get("Referrer-Policy"), hdr.Get("Cache-Control"))
		}
		if body := w.Body.String(); strings.Contains(body, secret) || strings.Contains(body, "apollo") {
			t.Errorf("wrong key %d: the body shows the secret or the list", i)
		}
	}
}

func TestDocumentResponseSetsCookieOnlyOnSuccess(t *testing.T) {
	secret := freshSecret(t)
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/d/abc123/plan.md":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("doc"))
		case "/d/abc123/implicit.md":
			w.Write([]byte("doc"))
		case "/d/abc123":
			w.Header().Set("Location", "/d/abc123/")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/d/abc123/down.md":
			http.Error(w, "not connected", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}), nil)
	for _, path := range []string{"/d/abc123/plan.md", "/d/abc123/implicit.md"} {
		w := serveHome(t, "1.2.3", "GET", path)
		if got := w.Header().Values("Set-Cookie"); w.Code != 200 || len(got) != 1 || got[0] != pageCookieHeader(secret) {
			t.Errorf("GET %s: status %d, %d Set-Cookie values, want 200 with the page cookie", path, w.Code, len(got))
		}
	}
	for _, path := range []string{"/d/nope/plan.md", "/d/abc123", "/d/abc123/down.md"} {
		w := serveHome(t, "1.2.3", "GET", path)
		if w.Code == 200 || len(w.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("GET %s: status %d, Set-Cookie %d values, want a failure with no cookie", path, w.Code, len(w.Header().Values("Set-Cookie")))
		}
	}
}
