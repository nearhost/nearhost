package proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/getkin/kin-openapi/openapi3"

	nhapi "github.com/nearhost/nearhost/api"
	"github.com/nearhost/nearhost/internal/devices"
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
	r.Header.Set("X-Nearhost-CSRF", derivedAPIToken(t, "page"))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// derivedAPIToken computes the API token of subject from the page secret,
// as the schema describes it, without going through the session.
func derivedAPIToken(t *testing.T, subject string) string {
	t.Helper()
	secret, err := pageSecret()
	if err != nil {
		t.Fatalf("pageSecret: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("nearhost api token\x00" + subject))
	return hex.EncodeToString(mac.Sum(nil))
}

// pageAPIToken is the API token the session gives the page cookie.
func pageAPIToken(t *testing.T, secret string) string {
	t.Helper()
	return sessionOf(t, serveHomeRequest(t, withCookie("/api/v1/session", secret))).CSRF
}

// deviceAPIToken is the API token the session gives the device whose
// cookie is token.
func deviceAPIToken(t *testing.T, token string) string {
	t.Helper()
	return sessionOf(t, serveNet(t, netHome(), withDevice(netRequest("GET", "/api/v1/session", nil), token))).CSRF
}

// deviceWrite is a write to path from the device whose cookie is token,
// carrying the API token csrf.
func deviceWrite(token, csrf string) *http.Request {
	r := withDevice(netRequest("POST", "/api/v1/projects/app/start", strings.NewReader("{}")), token)
	r.Header.Set("Origin", "https://"+netPageHost+":7443")
	r.Header.Set("X-Nearhost-CSRF", csrf)
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
	r := deviceWrite("good-token", deviceAPIToken(t, "good-token"))
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
		"other-port Origin":            func(r *http.Request) { r.Header.Set("Origin", "https://nearhost.localhost:3000") },
		"Origin null":                  func(r *http.Request) { r.Header.Set("Origin", "null") },
		"no token":                     func(r *http.Request) { r.Header.Del("X-Nearhost-CSRF") },
		"wrong token":                  func(r *http.Request) { r.Header.Set("X-Nearhost-CSRF", wrongSecret(pageAPIToken(t, secret))) },
		"two tokens":                   func(r *http.Request) { r.Header.Add("X-Nearhost-CSRF", pageAPIToken(t, secret)) },
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

// sessionOf decodes the body of an answer to GET /api/v1/session.
func sessionOf(t *testing.T, w *httptest.ResponseRecorder) Session {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	var s Session
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("body is not a session: %v\n%s", err, w.Body.String())
	}
	return s
}

func TestSessionCarriesTheToken(t *testing.T) {
	secret := freshSecret(t)
	fake := setFakeAPI(t)
	w := serveHomeRequest(t, withCookie("/api/v1/session", secret))
	wantAPIHeaders(t, "session", w)
	s := sessionOf(t, w)
	if s.CSRF == "" || s.CSRF == mustToken(t) {
		t.Errorf("csrf %q, want a token that is not the form token", s.CSRF)
	}
	if s.Device != "" || s.Networked {
		t.Errorf("session %+v, want no device and not networked", s)
	}
	if got := fake.called(); len(got) != 0 {
		t.Errorf("the handler was called: %v", got)
	}
}

func TestSessionNamesThePairedDevice(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	setFakeAPI(t)
	h := netHome()
	h.Machine = "leto"
	s := sessionOf(t, serveNet(t, h, withDevice(netRequest("GET", "/api/v1/session", nil), "good-token")))
	if s.Device != "iPad" || !s.Networked || s.Machine != "leto" {
		t.Errorf("session %+v, want device iPad, networked, machine leto", s)
	}
}

func TestSessionNeedsACredential(t *testing.T) {
	secret := freshSecret(t)
	setFakeDevices(t)
	setFakeAPI(t)
	w := serveHomeRequest(t, withCookie("/api/v1/session", wrongSecret(secret)))
	wantProblem(t, "no cookie", w, http.StatusUnauthorized, "proxy.api-sign-in")
	w = serveNet(t, netHome(), netRequest("GET", "/api/v1/session", nil))
	wantProblem(t, "no device", w, http.StatusUnauthorized, "proxy.api-sign-in")
}

func TestSessionTakesGetAndHeadOnly(t *testing.T) {
	secret := freshSecret(t)
	setFakeAPI(t)
	r := withCookie("/api/v1/session", secret)
	r.Method = "HEAD"
	if w := serveHomeRequest(t, r); w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD: status %d, body %q", w.Code, w.Body.String())
	}
	r = apiWrite(t, "POST", "/api/v1/session", secret, strings.NewReader("{}"))
	w := serveHomeRequest(t, r)
	wantProblem(t, "POST", w, http.StatusMethodNotAllowed, "proxy.method-not-allowed")
	if a := w.Header().Get("Allow"); a != "GET, HEAD" {
		t.Errorf("Allow %q", a)
	}
}

func TestSessionMatchesTheSchema(t *testing.T) {
	secret := freshSecret(t)
	setFakeAPI(t)
	doc, err := openapi3.NewLoader().LoadFromData(nhapi.Schema)
	if err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["Session"]
	if schema == nil {
		t.Fatal("the schema has no Session")
	}
	setFakeDevices(t)
	for _, w := range []*httptest.ResponseRecorder{
		serveHomeRequest(t, withCookie("/api/v1/session", secret)),
		serveNet(t, netHome(), withDevice(netRequest("GET", "/api/v1/session", nil), "good-token")),
	} {
		var v any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
		if err := schema.Value.VisitJSON(v); err != nil {
			t.Errorf("body %s does not match the schema: %v", w.Body.String(), err)
		}
	}
}

// netWrite is a write at the network host from a paired device, with the
// token and JSON, and the given Origin.
func netWrite(t *testing.T, origin string) *http.Request {
	t.Helper()
	r := withDevice(netRequest("POST", "/api/v1/projects/app/stop", strings.NewReader("{}")), "good-token")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Nearhost-CSRF", derivedAPIToken(t, "device:dev1"))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestAPIWritesAtTheNetworkHostNeedItsOwnOrigin(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	fake := setFakeAPI(t)
	for name, origin := range map[string]string{
		"sibling site":  "https://shop.lab.example.dev:7443",
		"other port":    "https://" + netPageHost + ":3000",
		"no port":       "https://" + netPageHost,
		"plain http":    "http://" + netPageHost + ":7443",
		"the page host": "https://nearhost.localhost:7443",
	} {
		w := serveNet(t, netHome(), netWrite(t, origin))
		wantProblem(t, name, w, http.StatusForbidden, "proxy.api-write-refused")
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Fatalf("the API was called: %v", calls)
	}
	if w := serveNet(t, netHome(), netWrite(t, "https://"+netPageHost+":7443")); w.Code != 200 {
		t.Errorf("own origin: status %d, body %s", w.Code, w.Body.String())
	}
	if calls := fake.called(); len(calls) != 1 {
		t.Errorf("API calls %v, want 1", calls)
	}
}

func TestSessionHeadNeedsACredential(t *testing.T) {
	freshSecret(t)
	setFakeAPI(t)
	r := pageRequest("HEAD", "/api/v1/session")
	if w := serveHomeRequest(t, r); w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", w.Code)
	}
}

// streamAPI is an API handler that holds every request open until its
// context ends, as the event stream does, and then closes ended.
type streamAPI struct {
	started chan struct{}
	ended   chan struct{}
}

func (s *streamAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	close(s.started)
	<-r.Context().Done()
	close(s.ended)
}

// openStream sets a streamAPI, sends r to h through the gate in the
// background and waits for the stream to open. It returns the handler and
// a channel closed once ServeHTTP returns.
func openStream(t *testing.T, h *Home, r *http.Request) (*streamAPI, chan struct{}) {
	t.Helper()
	old := apiRecheck
	apiRecheck = 20 * time.Millisecond
	t.Cleanup(func() { apiRecheck = old })
	s := &streamAPI{started: make(chan struct{}), ended: make(chan struct{})}
	t.Cleanup(func() { SetAPI(nil) })
	SetAPI(s)
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	ctx, cancel := context.WithCancel(r.Context())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx), nil)
	}()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not open")
	}
	return s, done
}

// wantEnds fails the test unless the stream ends within a few rechecks.
func wantEnds(t *testing.T, s *streamAPI, done chan struct{}) {
	t.Helper()
	select {
	case <-s.ended:
	case <-time.After(apiRecheck * 50):
		t.Fatal("the stream did not end once its credential stopped being valid")
	}
	<-done
}

func TestAPIStreamEndsWhenItsDeviceIsRevoked(t *testing.T) {
	freshSecret(t)
	devs := setFakeDevices(t)
	s, done := openStream(t, netHome(), withDevice(netRequest("GET", "/api/v1/events", nil), "good-token"))
	select {
	case <-s.ended:
		t.Fatal("the stream ended while the device was still paired")
	case <-time.After(apiRecheck * 5):
	}
	devs.mu.Lock()
	delete(devs.devices, "good-token")
	devs.mu.Unlock()
	wantEnds(t, s, done)
}

func TestAPIStreamEndsWhenThePageSecretChanges(t *testing.T) {
	secret := freshSecret(t)
	s, done := openStream(t, &Home{Version: "1.2.3"}, withCookie("/api/v1/events", secret))
	freshSecret(t)
	wantEnds(t, s, done)
}

func TestAPIRequestLeavesNoWatcherRunning(t *testing.T) {
	secret := freshSecret(t)
	setFakeAPI(t)
	old := apiRecheck
	apiRecheck = time.Millisecond
	t.Cleanup(func() { apiRecheck = old })
	for range 20 {
		if w := serveHomeRequest(t, withCookie("/api/v1/state", secret)); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		if n := apiWatchers.Load(); n != 0 {
			t.Fatalf("%d credential watchers still running after the request returned", n)
		}
	}
}

func TestAPITokenIsPerDevice(t *testing.T) {
	secret := freshSecret(t)
	devs := setFakeDevices(t)
	devs.mu.Lock()
	devs.devices["other-token"] = devices.Device{Name: "Phone", ID: "dev2"}
	devs.mu.Unlock()
	fake := setFakeAPI(t)
	a, b := deviceAPIToken(t, "good-token"), deviceAPIToken(t, "other-token")
	page := pageAPIToken(t, secret)
	if a == "" || a == b || a == page || b == page {
		t.Fatalf("tokens must differ: a %q b %q page %q", a, b, page)
	}
	if a != derivedAPIToken(t, "device:dev1") || b != derivedAPIToken(t, "device:dev2") || page != derivedAPIToken(t, "page") {
		t.Errorf("tokens are not HMAC(secret, label+subject)")
	}
	if a != deviceAPIToken(t, "good-token") {
		t.Errorf("a device's token changed between sessions")
	}
	if w := serveNet(t, netHome(), deviceWrite("good-token", a)); w.Code != 200 {
		t.Errorf("A with A's token: status %d", w.Code)
	}
	for what, csrf := range map[string]string{"B's token": b, "the page's token": page, "the form token": mustToken(t)} {
		w := serveNet(t, netHome(), deviceWrite("good-token", csrf))
		wantProblem(t, "A with "+what, w, http.StatusForbidden, "proxy.api-write-refused")
	}
	if w := serveNet(t, netHome(), deviceWrite("other-token", b)); w.Code != 200 {
		t.Errorf("B with B's token: status %d", w.Code)
	}
	if w := serveNet(t, netHome(), deviceWrite("other-token", a)); w.Code != 403 {
		t.Errorf("B with A's token: status %d, want 403", w.Code)
	}
	// The page cookie's token works for the page cookie, not for a device.
	if w := serveHomeRequest(t, apiWrite(t, "POST", "/api/v1/projects/app/stop", secret, strings.NewReader("{}"))); w.Code != 200 {
		t.Errorf("page write: status %d", w.Code)
	}
	r := apiWrite(t, "POST", "/api/v1/projects/app/stop", secret, strings.NewReader("{}"))
	r.Header.Set("X-Nearhost-CSRF", a)
	wantProblem(t, "page with A's token", serveHomeRequest(t, r), http.StatusForbidden, "proxy.api-write-refused")
	if got := len(fake.called()); got != 3 {
		t.Errorf("API calls %d, want 3", got)
	}
}

func TestAPITokenChangesWhenADeviceIsPairedAgain(t *testing.T) {
	freshSecret(t)
	devs := setFakeDevices(t)
	setFakeAPI(t)
	old := deviceAPIToken(t, "good-token")
	devs.mu.Lock()
	devs.devices["good-token"] = devices.Device{Name: "iPad", ID: "dev9"}
	devs.mu.Unlock()
	now := deviceAPIToken(t, "good-token")
	if now == old {
		t.Fatal("a device paired again kept its token")
	}
	wantProblem(t, "old token", serveNet(t, netHome(), deviceWrite("good-token", old)), http.StatusForbidden, "proxy.api-write-refused")
	if w := serveNet(t, netHome(), deviceWrite("good-token", now)); w.Code != 200 {
		t.Errorf("new token: status %d", w.Code)
	}
}
