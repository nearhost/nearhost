package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/nearhost/nearhost/internal/hostname"
)

// fakeConsole records the requests it is sent and answers each 200 with
// "console".
type fakeConsole struct {
	mu    sync.Mutex
	calls []string
	// csrf is the form field csrf as the console saw it, last call.
	csrf string
}

func (f *fakeConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Method == http.MethodPost {
		f.csrf = r.PostFormValue("csrf")
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("console"))
}

func (f *fakeConsole) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// setFakeConsole sets a fake console for the test and returns it.
func setFakeConsole(t *testing.T) *fakeConsole {
	t.Helper()
	f := new(fakeConsole)
	t.Cleanup(func() { SetConsole(nil) })
	SetConsole(f)
	return f
}

// consolePost is a POST of form to path at Nearhost's page, carrying the
// page cookie with secret.
func consolePost(path, secret string, form url.Values) *http.Request {
	r := pageRequestWith("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	return r
}

// pageRequestWith is pageRequest with body.
func pageRequestWith(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = hostname.PageHost + ":6327"
	return r
}

func TestConsoleNeedsTheCookie(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeConsole(t)
	for _, method := range []string{"GET", "HEAD"} {
		for name, r := range map[string]*http.Request{
			"no cookie":             pageRequest(method, "/approve/abc"),
			"wrong cookie":          withCookieMethod(method, "/approve/abc", wrongSecret(secret)),
			"localhost with cookie": localhostWithCookie(method, "/approve/abc", secret),
		} {
			w := serveHomeRequest(t, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, want 403", method, name, w.Code)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != homeCSP {
				t.Errorf("%s %s: CSP %q, want homeCSP", method, name, got)
			}
			body := w.Body.String()
			if method == "HEAD" {
				if body != "" {
					t.Errorf("HEAD %s: a body", name)
				}
				continue
			}
			if !strings.Contains(body, "Open Nearhost&#39;s page from this machine first") && !strings.Contains(body, "Open Nearhost's page from this machine first") {
				t.Errorf("GET %s: body lacks the heading:\n%s", name, body)
			}
			if !strings.Contains(body, "Run nearhost status here and open the page address it prints, or click the page address in nearhost. Then open this link again.") {
				t.Errorf("GET %s: body lacks the advice", name)
			}
			if strings.Contains(body, "abc") {
				t.Errorf("GET %s: the body names the request", name)
			}
		}
	}
	// The other console paths are locked alike, and none says which it was.
	for _, p := range []string{"/change/myapp", "/events", "/page.js"} {
		w := serveHomeRequest(t, pageRequest("GET", p))
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), strings.TrimPrefix(p, "/")) {
			t.Errorf("GET %s: status %d or the body names the path", p, w.Code)
		}
	}
	// A POST without the cookie is a plain 403, whatever else it carries.
	form := url.Values{"csrf": {mustToken(t)}}
	r := pageRequestWith("POST", "/approve/abc", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://"+r.Host)
	w := serveHomeRequest(t, r)
	if w.Code != http.StatusForbidden || strings.TrimSpace(w.Body.String()) != "Forbidden" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("POST without the cookie: status %d, %q, %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Errorf("the console was called: %v", calls)
	}
}

func TestConsolePostNeedsOriginAndToken(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeConsole(t)
	token := mustToken(t)
	const host = "https://nearhost.localhost:6327"

	refused := map[string]func() *http.Request{
		"no token": func() *http.Request {
			r := consolePost("/approve/abc", secret, url.Values{})
			r.Header.Set("Origin", host)
			return r
		},
		"wrong token": func() *http.Request {
			r := consolePost("/approve/abc", secret, url.Values{"csrf": {wrongSecret(token)}})
			r.Header.Set("Origin", host)
			return r
		},
		"token in the query only": func() *http.Request {
			r := consolePost("/approve/abc?csrf="+token, secret, url.Values{})
			r.Header.Set("Origin", host)
			return r
		},
		"another origin": func() *http.Request {
			r := consolePost("/approve/abc", secret, url.Values{"csrf": {token}})
			r.Header.Set("Origin", "https://app.test")
			return r
		},
		"origin null": func() *http.Request {
			r := consolePost("/approve/abc", secret, url.Values{"csrf": {token}})
			r.Header.Set("Origin", "null")
			return r
		},
		"another port": func() *http.Request {
			r := consolePost("/approve/abc", secret, url.Values{"csrf": {token}})
			r.Header.Set("Origin", "https://nearhost.localhost:3000")
			return r
		},
		"no origin, cross-site": func() *http.Request {
			r := consolePost("/approve/abc", secret, url.Values{"csrf": {token}})
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		},
		"no origin, no fetch site": func() *http.Request {
			return consolePost("/approve/abc", secret, url.Values{"csrf": {token}})
		},
	}
	for name, mk := range refused {
		w := serveHomeRequest(t, mk())
		if w.Code != http.StatusForbidden || strings.TrimSpace(w.Body.String()) != "Forbidden" {
			t.Errorf("%s: status %d, body %q, want 403 Forbidden", name, w.Code, w.Body.String())
		}
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Fatalf("the console was called for a refused POST: %v", calls)
	}

	r := consolePost("/approve/abc", secret, url.Values{"csrf": {token}})
	r.Header.Set("Origin", host)
	if w := serveHomeRequest(t, r); w.Code != 200 || w.Body.String() != "console" {
		t.Errorf("matching Origin and token: status %d, body %q", w.Code, w.Body.String())
	}
	r = consolePost("/change/myapp", secret, url.Values{"csrf": {token}})
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := serveHomeRequest(t, r); w.Code != 200 || w.Body.String() != "console" {
		t.Errorf("same-origin fetch and token: status %d, body %q", w.Code, w.Body.String())
	}
	if got, want := strings.Join(fake.called(), ","), "POST /approve/abc,POST /change/myapp"; got != want {
		t.Errorf("console calls = %s, want %s", got, want)
	}
	if fake.csrf != token {
		t.Errorf("the console saw csrf %q, want the form's", fake.csrf)
	}
}

func TestConsoleMethods(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeConsole(t)
	token := mustToken(t)
	for _, p := range []string{"/", "/events", "/page.js"} {
		r := consolePost(p, secret, url.Values{"csrf": {token}})
		r.Header.Set("Origin", "https://"+r.Host)
		if w := serveHomeRequest(t, r); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: status %d, want 405", p, w.Code)
		}
	}
	for _, p := range []string{"/approve/abc", "/approve/a/b", "/approve/", "/change/", "/events/x"} {
		r := withCookieMethod("DELETE", p, secret)
		if w := serveHomeRequest(t, r); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
			t.Errorf("DELETE %s: status %d", p, w.Code)
		}
	}
	for _, p := range []string{"/approve/a/b", "/approve/", "/events/x", "/nope"} {
		if w := serveHomeRequest(t, withCookie(p, secret)); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, w.Code)
		}
	}
	for _, p := range []string{"/", "/approve/abc", "/change/myapp", "/events", "/page.js"} {
		for _, m := range []string{"GET", "HEAD"} {
			if w := serveHomeRequest(t, withCookieMethod(m, p, secret)); w.Code != 200 {
				t.Errorf("%s %s: status %d, want 200", m, p, w.Code)
			}
		}
	}
	if n := len(fake.called()); n != 10 {
		t.Errorf("console called %d times, want 10: %v", n, fake.called())
	}
}

func TestConsoleHeaders(t *testing.T) {
	secret := freshSecret(t)
	setFakeConsole(t)
	for _, p := range []string{"/", "/approve/abc"} {
		w := serveHomeRequest(t, withCookie(p, secret))
		if w.Code != 200 || w.Body.String() != "console" {
			t.Fatalf("GET %s: status %d, body %q", p, w.Code, w.Body.String())
		}
		for k, v := range map[string]string{
			"Content-Security-Policy": consoleCSP,
			"X-Content-Type-Options":  "nosniff",
			"Cache-Control":           "no-store",
			"Referrer-Policy":         "no-referrer",
			"Vary":                    "Cookie",
		} {
			if got := w.Header().Get(k); got != v {
				t.Errorf("GET %s: %s = %q, want %q", p, k, got, v)
			}
		}
	}
}

func TestHomeUnchangedWithoutConsole(t *testing.T) {
	secret := freshSecret(t)
	listTrees(t)
	before := serveHomeRequest(t, withCookie("/", secret))
	if before.Code != 200 || !strings.Contains(before.Body.String(), "docs/plan.md") {
		t.Fatalf("GET / with the cookie: status %d", before.Code)
	}
	if got := before.Header().Get("Content-Security-Policy"); got != homeCSP {
		t.Errorf("CSP = %q, want homeCSP", got)
	}
	// With no console the other console paths are not found, as today.
	if w := serveHomeRequest(t, withCookie("/approve/abc", secret)); w.Code != http.StatusNotFound {
		t.Errorf("GET /approve/abc with no console: status %d, want 404", w.Code)
	}

	// With a console set, / without the cookie is still today's page, and
	// the console is not called.
	fake := setFakeConsole(t)
	plain := serveHomeRequest(t, pageRequest("GET", "/"))
	if plain.Code != 200 || plain.Header().Get("Content-Security-Policy") != homeCSP || !strings.Contains(plain.Body.String(), "Run it anywhere.") {
		t.Errorf("GET / without the cookie: status %d, CSP %q", plain.Code, plain.Header().Get("Content-Security-Policy"))
	}
	if w := serveHomeRequest(t, localhostWithCookie("GET", "/", secret)); w.Code != 200 || w.Body.String() == "console" {
		t.Errorf("GET / at localhost: status %d, body %q", w.Code, w.Body.String())
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Errorf("the console was called: %v", calls)
	}

	SetConsole(nil)
	after := serveHomeRequest(t, withCookie("/", secret))
	if after.Body.String() != before.Body.String() {
		t.Error("clearing the console changed the page")
	}
}

func TestKeyRedirectsToNext(t *testing.T) {
	secret := freshSecret(t)
	key := func(next string) *http.Request {
		return pageRequest("GET", "/?"+url.Values{"k": {secret}, "next": {next}}.Encode())
	}
	w := serveHomeRequest(t, key("/approve/abc"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/approve/abc" {
		t.Errorf("next=/approve/abc: status %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	if got := w.Header().Get("Set-Cookie"); got != pageCookieHeader(secret) {
		t.Errorf("Set-Cookie = %q", got)
	}
	if w := serveHomeRequest(t, key("/change/myapp")); w.Header().Get("Location") != "/change/myapp" {
		t.Errorf("next=/change/myapp: Location %q", w.Header().Get("Location"))
	}
	for _, next := range []string{"https://evil.test/", "//evil.test", "/d/x", "/approve/a/b", "/approve/", "/approve/a?x=1", "/approve/a#x", "/events", "/\\evil.test", ""} {
		w := serveHomeRequest(t, key(next))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
			t.Errorf("next=%q: status %d, Location %q, want /", next, w.Code, w.Header().Get("Location"))
		}
	}
	// A wrong key still redirects, and sets nothing.
	r := pageRequest("GET", "/?"+url.Values{"k": {wrongSecret(secret)}, "next": {"/approve/abc"}}.Encode())
	w = serveHomeRequest(t, r)
	if w.Header().Get("Location") != "/approve/abc" || w.Header().Get("Set-Cookie") != "" {
		t.Errorf("wrong key: Location %q, Set-Cookie %q", w.Header().Get("Location"), w.Header().Get("Set-Cookie"))
	}
}

func TestCSRFTokenIsStable(t *testing.T) {
	secret := freshSecret(t)
	a, err := CSRFToken()
	if err != nil {
		t.Fatalf("CSRFToken: %v", err)
	}
	b, err := CSRFToken()
	if err != nil {
		t.Fatalf("CSRFToken: %v", err)
	}
	if a != b {
		t.Errorf("two tokens: %q, %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("token %q is %d characters, want 64", a, len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("token %q is not hex", a)
	}
	if a == secret {
		t.Error("the token is the secret")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("nearhost page form"))
	if want := hex.EncodeToString(mac.Sum(nil)); a != want {
		t.Errorf("token = %q, want %q", a, want)
	}
}

// mustToken returns CSRFToken or fails the test.
func mustToken(t *testing.T) string {
	t.Helper()
	token, err := CSRFToken()
	if err != nil {
		t.Fatalf("CSRFToken: %v", err)
	}
	return token
}

// withCookieMethod is withCookie for method.
func withCookieMethod(method, path, value string) *http.Request {
	r := pageRequest(method, path)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: value})
	return r
}

// localhostWithCookie is a request at localhost carrying the page cookie
// with value, as a browser would only if some server there had set one.
func localhostWithCookie(method, path, value string) *http.Request {
	r := localhostRequest(method, path)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: value})
	return r
}
