package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI records the requests it is sent, reads each body to the end, and
// answers 200 with "api", or 413 when the body is over the gate's cap.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string
	// read is how many bytes of the body the last call read.
	read int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.read = len(b)
	f.mu.Unlock()
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`"api"`))
}

func (f *fakeAPI) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// setFakeAPI sets a fake API handler for the test and returns it.
func setFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := new(fakeAPI)
	t.Cleanup(func() { SetAPI(nil) })
	SetAPI(f)
	return f
}

// apiWrite is a write to path at Nearhost's page with the page cookie,
// the page's origin, the form token and a JSON body: everything the gate
// asks of a write.
func apiWrite(t *testing.T, method, path, secret string, body io.Reader) *http.Request {
	t.Helper()
	r := pageRequestWith(method, path, body)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	r.Header.Set("Origin", "https://"+r.Host)
	r.Header.Set("X-Nearhost-CSRF", mustToken(t))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// wantProblem fails the test unless w is a JSON problem with status and
// code.
func wantProblem(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Errorf("%s: status %d, want %d; body %s", what, w.Code, status, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("%s: Content-Type %q, want application/problem+json", what, ct)
	}
	var p map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Errorf("%s: body is not JSON: %v\n%s", what, err, w.Body.String())
		return
	}
	if p["code"] != code {
		t.Errorf("%s: code %v, want %s", what, p["code"], code)
	}
	if p["status"] != float64(status) {
		t.Errorf("%s: problem status %v, want %d", what, p["status"], status)
	}
	if p["error"] == nil || p["title"] == nil || p["type"] == nil {
		t.Errorf("%s: problem lacks error, title or type: %s", what, w.Body.String())
	}
}

// wantAPIHeaders fails the test unless w carries every header the gate
// gives an answer under /api/.
func wantAPIHeaders(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	for k, v := range map[string]string{
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
		"Vary":                    "Cookie",
		"Referrer-Policy":         "same-origin",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s: %s %q, want %q", what, k, got, v)
		}
	}
}

func TestAPINeedsACredential(t *testing.T) {
	secret := freshSecret(t)
	setFakeDevices(t)
	fake := setFakeAPI(t)
	var bodies []string
	for _, r := range []*http.Request{
		pageRequest("GET", "/api/v1/state"),
		pageRequest("GET", "/api/v1/stop"),
		pageRequest("GET", "/api/nothing/here"),
		pageRequest("GET", "/api"),
		pageRequest("HEAD", "/api/v1/state"),
		withCookieMethod("GET", "/api/v1/state", wrongSecret(secret)),
		localhostWithCookie("GET", "/api/v1/state", secret),
		netRequest("GET", "/api/v1/state", nil),
		netRequest("GET", "/api/v1/events", nil),
		withDevice(netRequest("GET", "/api/v1/state", nil), "bad-token"),
	} {
		what := r.Method + " " + r.Host + r.URL.Path
		h := &Home{Version: "1.2.3"}
		if r.Host != pageRequest("GET", "/").Host && !strings.HasPrefix(r.Host, "localhost") {
			h = netHome()
		}
		w := serveNet(t, h, r)
		if r.Method == "HEAD" {
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s: status %d, want 401", what, w.Code)
			}
			continue
		}
		wantProblem(t, what, w, http.StatusUnauthorized, "proxy.api-sign-in")
		wantAPIHeaders(t, what, w)
		bodies = append(bodies, w.Body.String())
	}
	// A POST with everything but a credential is a 401 too.
	r := apiWrite(t, "POST", "/api/v1/stop", wrongSecret(secret), strings.NewReader("{}"))
	w := serveHomeRequest(t, r)
	wantProblem(t, "POST without the cookie", w, http.StatusUnauthorized, "proxy.api-sign-in")
	bodies = append(bodies, w.Body.String())
	for i, b := range bodies {
		if b != bodies[0] {
			t.Errorf("answer %d differs from the first:\n%s\n%s", i, b, bodies[0])
		}
		if strings.Contains(b, "<") || strings.Contains(b, "state") || strings.Contains(b, "stop") || strings.Contains(b, "/pair") {
			t.Errorf("answer %d names the path or is HTML: %s", i, b)
		}
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Errorf("the API was called: %v", calls)
	}
}

func TestAPIAcceptsThePageCookie(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeAPI(t)
	w := serveHomeRequest(t, withCookie("/api/v1/state", secret))
	if w.Code != 200 || w.Body.String() != `"api"` {
		t.Fatalf("GET with the cookie: status %d, body %q", w.Code, w.Body.String())
	}
	if got := fake.called(); len(got) != 1 || got[0] != "GET /api/v1/state" {
		t.Errorf("API calls %v", got)
	}
}

func TestAPIAcceptsAPairedDevice(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	fake := setFakeAPI(t)
	w := serveNet(t, netHome(), withDevice(netRequest("GET", "/api/v1/state", nil), "good-token"))
	if w.Code != 200 || w.Body.String() != `"api"` {
		t.Fatalf("GET from a paired device: status %d, body %q", w.Code, w.Body.String())
	}
	r := withDevice(netRequest("POST", "/api/v1/projects/app/start", strings.NewReader("{}")), "good-token")
	r.Header.Set("Origin", "https://"+netPageHost+":7443")
	r.Header.Set("X-Nearhost-CSRF", mustToken(t))
	r.Header.Set("Content-Type", "application/json")
	if w := serveNet(t, netHome(), r); w.Code != 200 {
		t.Fatalf("POST from a paired device: status %d, body %q", w.Code, w.Body.String())
	}
	if got := fake.called(); len(got) != 2 || got[1] != "POST /api/v1/projects/app/start" {
		t.Errorf("API calls %v", got)
	}
}

func TestAPIRefusesARevokedDevice(t *testing.T) {
	freshSecret(t)
	devs := setFakeDevices(t)
	fake := setFakeAPI(t)
	if w := serveNet(t, netHome(), withDevice(netRequest("GET", "/api/v1/events", nil), "good-token")); w.Code != 200 {
		t.Fatalf("before revoking: status %d", w.Code)
	}
	devs.mu.Lock()
	delete(devs.devices, "good-token")
	devs.mu.Unlock()
	w := serveNet(t, netHome(), withDevice(netRequest("GET", "/api/v1/events", nil), "good-token"))
	wantProblem(t, "after revoking", w, http.StatusUnauthorized, "proxy.api-sign-in")
	if got := fake.called(); len(got) != 1 {
		t.Errorf("API calls %v, want the one before revoking", got)
	}
}

func TestAPIWritesNeedOriginTokenAndJSON(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeAPI(t)
	const path = "/api/v1/projects/app/stop"
	for name, change := range map[string]func(r *http.Request){
		"no Origin, no Sec-Fetch-Site": func(r *http.Request) { r.Header.Del("Origin") },
		"no Origin, cross-site":        func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"foreign Origin":               func(r *http.Request) { r.Header.Set("Origin", "https://app.test") },
		"Origin null":                  func(r *http.Request) { r.Header.Set("Origin", "null") },
		"no token":                     func(r *http.Request) { r.Header.Del("X-Nearhost-CSRF") },
		"wrong token":                  func(r *http.Request) { r.Header.Set("X-Nearhost-CSRF", wrongSecret(mustToken(t))) },
		"two tokens":                   func(r *http.Request) { r.Header.Add("X-Nearhost-CSRF", mustToken(t)) },
		"no Content-Type":              func(r *http.Request) { r.Header.Del("Content-Type") },
		"text/plain":                   func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"form":                         func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"multipart":                    func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=x") },
		"json-ish":                     func(r *http.Request) { r.Header.Set("Content-Type", "application/json-seq") },
		"bad media type":               func(r *http.Request) { r.Header.Set("Content-Type", "application/json; ;") },
	} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			r := apiWrite(t, method, path, secret, strings.NewReader("{}"))
			change(r)
			w := serveHomeRequest(t, r)
			what := method + " " + name
			wantProblem(t, what, w, http.StatusForbidden, "proxy.api-write-refused")
			wantAPIHeaders(t, what, w)
		}
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Fatalf("the API was called: %v", calls)
	}
	for name, change := range map[string]func(r *http.Request){
		"all present":           func(*http.Request) {},
		"charset":               func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") },
		"upper case":            func(r *http.Request) { r.Header.Set("Content-Type", "Application/JSON") },
		"Sec-Fetch-Site only":   func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Sec-Fetch-Site", "same-origin") },
		"Origin and fetch site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") },
	} {
		r := apiWrite(t, "POST", path, secret, strings.NewReader("{}"))
		change(r)
		if w := serveHomeRequest(t, r); w.Code != 200 {
			t.Errorf("%s: status %d, body %s", name, w.Code, w.Body.String())
		}
	}
	if calls := fake.called(); len(calls) != 5 {
		t.Errorf("API calls %v, want 5", calls)
	}
}

func TestAPIReadsNeedNoToken(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeAPI(t)
	for _, method := range []string{"GET", "HEAD"} {
		w := serveHomeRequest(t, withCookieMethod(method, "/api/v1/state", secret))
		if w.Code != 200 {
			t.Errorf("%s with the cookie alone: status %d", method, w.Code)
		}
	}
	if got := fake.called(); len(got) != 2 {
		t.Errorf("API calls %v", got)
	}
}

func TestAPIHeaders(t *testing.T) {
	secret := freshSecret(t)
	// A handler that answers 404 itself, as the control API does for a
	// denied route, still gets the gate's headers.
	t.Cleanup(func() { SetAPI(nil) })
	SetAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/missing" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("{}"))
	}))
	cases := map[string]*http.Request{
		"200":         withCookie("/api/v1/state", secret),
		"handler 404": withCookie("/api/v1/missing", secret),
		"401":         pageRequest("GET", "/api/v1/state"),
		"403":         withCookieMethod("POST", "/api/v1/stop", secret),
	}
	for name, r := range cases {
		wantAPIHeaders(t, name, serveHomeRequest(t, r))
	}
	SetAPI(nil)
	wantAPIHeaders(t, "no handler", serveHomeRequest(t, withCookie("/api/v1/state", secret)))
}

func TestAPIBodyIsCapped(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeAPI(t)
	big := bytes.Repeat([]byte("x"), 2<<20)
	w := serveHomeRequest(t, apiWrite(t, "POST", "/api/v1/projects/app/start", secret, bytes.NewReader(big)))
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("a 2 MiB body: status %d, want 413 or 400", w.Code)
	}
	fake.mu.Lock()
	read := fake.read
	fake.mu.Unlock()
	if read > 1<<20 {
		t.Errorf("the handler read %d bytes, over 1 MiB", read)
	}
	// A body at the cap passes.
	w = serveHomeRequest(t, apiWrite(t, "POST", "/api/v1/projects/app/start", secret, bytes.NewReader(big[:1<<20])))
	if w.Code != 200 {
		t.Errorf("a 1 MiB body: status %d", w.Code)
	}
}

func TestAPIWithoutAHandler(t *testing.T) {
	secret := freshSecret(t)
	SetAPI(nil)
	for _, r := range []*http.Request{
		withCookie("/api/v1/state", secret),
		apiWrite(t, "POST", "/api/v1/projects/app/start", secret, strings.NewReader("{}")),
	} {
		w := serveHomeRequest(t, r)
		wantProblem(t, r.Method+" with no handler", w, http.StatusNotFound, "control.no-route")
		wantAPIHeaders(t, r.Method+" with no handler", w)
	}
	// Set and then cleared, the same.
	setFakeAPI(t)
	SetAPI(nil)
	wantProblem(t, "cleared", serveHomeRequest(t, withCookie("/api/v1/state", secret)), http.StatusNotFound, "control.no-route")
}

func TestAPIPathsAreNotConsolePaths(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeAPI(t)
	setFakeConsole(t)
	for _, p := range []string{"/apix", "/api.js", "/apix/v1/state", "/API/v1/state", "/%61pi/v1/state"} {
		w := serveHomeRequest(t, withCookie(p, secret))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want the page's 404", p, w.Code)
		}
		if w.Header().Get("Content-Type") == "application/problem+json" {
			t.Errorf("GET %s: answered as the API", p)
		}
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Errorf("the API was called: %v", calls)
	}
	// /api and /api/ themselves are the API's, to answer as it will.
	for _, p := range []string{"/api", "/api/"} {
		serveHomeRequest(t, withCookie(p, secret))
	}
	if calls := fake.called(); len(calls) != 2 {
		t.Errorf("API calls %v, want /api and /api/", calls)
	}
}
