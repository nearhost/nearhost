package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

func registeredPatterns() []string {
	s := &Server{b: newActionFake(), hub: NewHub()}
	s.routes()
	return s.Patterns()
}

// Every registered route is either allowed on the network or denied, never
// both and never neither, and the sets name no route that is not registered.
func TestEveryRouteIsAllowedOrDenied(t *testing.T) {
	reg := registeredPatterns()
	for _, p := range reg {
		a, d := networkAllowed[p], networkDenied[p]
		if a && d {
			t.Errorf("%q is both allowed and denied", p)
		}
		if !a && !d {
			t.Errorf("%q is neither allowed nor denied on the network", p)
		}
	}
	for name, set := range map[string]map[string]bool{"allowed": networkAllowed, "denied": networkDenied} {
		for p := range set {
			if !slices.Contains(reg, p) {
				t.Errorf("%s entry %q is not a registered route", name, p)
			}
		}
	}
}

// The schema marks exactly the allowed operations. Operations marked
// x-nearhost-page are served by the page host, not the control socket, and
// are not control routes.
func TestNetworkMarksMatchTheAllowList(t *testing.T) {
	marked := map[string]bool{}
	for path, item := range loadSchema(t).Paths.Map() {
		for method, op := range item.Operations() {
			if page, _ := op.Extensions["x-nearhost-page"].(bool); page {
				continue
			}
			if net, _ := op.Extensions["x-nearhost-network"].(bool); net {
				marked[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	for p := range marked {
		if !networkAllowed[p] {
			t.Errorf("%q is marked x-nearhost-network but not allowed", p)
		}
	}
	for p := range networkAllowed {
		if !marked[p] {
			t.Errorf("%q is allowed but not marked x-nearhost-network in the schema", p)
		}
	}
}

func TestDeniedRoutesStayDenied(t *testing.T) {
	for _, p := range []string{"POST /v1/stop", "POST /v1/pair", "GET /v1/page", "GET /v1/captured/{id}"} {
		if NetworkAllowed(p) {
			t.Errorf("NetworkAllowed(%q) = true", p)
		}
	}
	if !NetworkAllowed("GET /v1/state") {
		t.Error("NetworkAllowed(GET /v1/state) = false")
	}
}

// serveNetwork starts a server for b and an HTTP server over its network
// handler, and returns both. They are closed, and the schema failures of
// both handlers' traffic reported, when the test ends.
func serveNetwork(t *testing.T, b Backend) (*Server, *httptest.Server) {
	t.Helper()
	s, err := Listen(SocketPath(testutil.TempDir(t, "ctl")), b, NewHub())
	if err != nil {
		t.Fatal(err)
	}
	checkTraffic(t, s)
	v, ok := s.netRoutes.(*validator)
	if !ok {
		t.Fatal("the network handler's routes are not wrapped by the schema validator")
	}
	t.Cleanup(func() {
		if !v.settle(5 * time.Second) {
			t.Errorf("schema: a network request was still being served 5s after the server closed")
		}
		for _, f := range v.report() {
			t.Errorf("schema (network): %s", f)
		}
	})
	ts := httptest.NewServer(s.NetworkHandler())
	t.Cleanup(ts.Close)
	return s, ts
}

// netDo sends method path to ts with an empty JSON body for writes, and
// returns the status and body.
func netDo(t *testing.T, ts *httptest.Server, method, path string) (int, string) {
	t.Helper()
	var body io.Reader
	if method != http.MethodGet {
		body = strings.NewReader("")
	}
	req, err := http.NewRequest(method, ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Don't follow redirects: a redirect is an answer in itself.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func TestNetworkHandlerServesAllowedRoutes(t *testing.T) {
	b := newActionFake()
	_, ts := serveNetwork(t, b)
	if code, body := netDo(t, ts, http.MethodGet, "/api/v1/state"); code != http.StatusOK {
		t.Fatalf("GET /api/v1/state = %d %s, want 200", code, body)
	}
	if code, body := netDo(t, ts, http.MethodPost, "/api/v1/projects/shop/start"); code/100 != 2 {
		t.Fatalf("POST /api/v1/projects/shop/start = %d %s, want 2xx", code, body)
	}
	if got, want := b.recorded(), []string{`StartProject "shop"`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the backend recorded %q, want %q", got, want)
	}
}

// notFoundBody is the body the network handler answers an unknown path
// with, with the path the client sent in place of the one asked for.
func notFoundBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	const unknown = "/api/v1/nothing-here"
	code, body := netDo(t, ts, http.MethodGet, unknown)
	if code != http.StatusNotFound {
		t.Fatalf("GET %s = %d, want 404", unknown, code)
	}
	if !strings.Contains(body, unknown) {
		t.Fatalf("the unknown path's 404 %s does not name the path", body)
	}
	return strings.ReplaceAll(body, unknown, path)
}

// A denied route answers just as an unknown path does, whatever the method,
// and reaches nothing.
func TestNetworkHandlerHidesDeniedRoutes(t *testing.T) {
	b := newActionFake()
	_, ts := serveNetwork(t, b)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/stop"},
		{http.MethodGet, "/api/v1/stop"},
		{http.MethodGet, "/api/v1/page"},
		{http.MethodPost, "/api/v1/page"},
		{http.MethodPost, "/api/v1/pair"},
		{http.MethodGet, "/api/v1/pair/abc"},
		{http.MethodGet, "/api/v1/environment"},
		{http.MethodPost, "/api/v1/documents/d1/open"},
		{http.MethodGet, "/api/v1/captured/x1"},
		{http.MethodGet, "/api/v1/session"},
	} {
		code, body := netDo(t, ts, tc.method, tc.path)
		if want := notFoundBody(t, ts, tc.path); code != http.StatusNotFound || body != want {
			t.Errorf("%s %s = %d %s, want 404 %s", tc.method, tc.path, code, body, want)
		}
	}
	if got := b.recorded(); len(got) != 0 {
		t.Fatalf("the backend recorded %q, want nothing", got)
	}
	if stops, changed := b.calls(); stops != 0 || changed != 0 {
		t.Fatalf("the backend was stopped %d times and told of hosts changes %d times", stops, changed)
	}
}

// A path that is not clean is not served, nor redirected: the mux would
// redirect it to its clean path without the prefix, and the clean path may
// be a denied route.
func TestNetworkHandlerRefusesUncleanPaths(t *testing.T) {
	b := newActionFake()
	_, ts := serveNetwork(t, b)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/../v1/stop"},
		{http.MethodPost, "/api/v1/%2e%2e/v1/stop"},
		{http.MethodPost, "/api/v1/%2E%2E/v1/stop"},
		{http.MethodPost, "/api/v1/./stop"},
		{http.MethodPost, "/api/v1//stop"},
		{http.MethodPost, "/api/v1/state/../stop"},
		{http.MethodPost, "/api/v1/../../v1/stop"},
		{http.MethodPost, "/api/./v1/stop"},
		{http.MethodPost, "/api%2Fv1/stop"},
		{http.MethodPost, "/api/v1%2Fstop"},
		{http.MethodGet, "/api/v1/../v1/page"},
		{http.MethodGet, "/api/v1/state/../environment"},
		{http.MethodGet, "/api/v1/../v1/state"},
		{http.MethodGet, "/api/v1/./state"},
		{http.MethodGet, "/api/v1//state"},
		{http.MethodPost, "/api/v1/projects/x/../shop/start"},
	} {
		req, err := http.NewRequest(tc.method, ts.URL, strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		// Opaque sends the path as written, unescaped and uncleaned.
		req.URL.Opaque = tc.path
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d (Location %q), want 404", tc.method, tc.path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if got := b.recorded(); len(got) != 0 {
		t.Fatalf("the backend recorded %q, want nothing", got)
	}
	if stops, _ := b.calls(); stops != 0 {
		t.Fatalf("the backend was stopped %d times", stops)
	}
}

// A wrong method on an allowed route answers 405, as on the socket.
func TestNetworkHandlerWrongMethod(t *testing.T) {
	_, ts := serveNetwork(t, newActionFake())
	if code, body := netDo(t, ts, http.MethodPost, "/api/v1/state"); code != http.StatusMethodNotAllowed || !strings.Contains(body, "/api/v1/state") {
		t.Fatalf("POST /api/v1/state = %d %s, want 405 naming /api/v1/state", code, body)
	}
}

func TestNetworkHandlerKeepsEscapedNames(t *testing.T) {
	b := newActionFake()
	_, ts := serveNetwork(t, b)
	if code, body := netDo(t, ts, http.MethodPost, "/api/v1/projects/a%2Fb/start"); code/100 != 2 {
		t.Fatalf("POST /api/v1/projects/a%%2Fb/start = %d %s, want 2xx", code, body)
	}
	if got, want := b.recorded(), []string{`StartProject "a/b"`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the backend recorded %q, want %q", got, want)
	}
}

func TestNetworkHandlerNeedsThePrefix(t *testing.T) {
	_, ts := serveNetwork(t, newActionFake())
	for _, p := range []string{"/v1/state", "/api/state", "/api/v1x/state", "/api", "/api/", "/apiv1/state", "/"} {
		code, body := netDo(t, ts, http.MethodGet, p)
		if want := notFoundBody(t, ts, p); code != http.StatusNotFound || body != want {
			t.Errorf("GET %s = %d %s, want 404 %s", p, code, body, want)
		}
	}
	if code, body := netDo(t, ts, http.MethodGet, "/api/v1"); code != http.StatusOK {
		t.Fatalf("GET /api/v1 = %d %s, want 200", code, body)
	}
	// A prefix written with escapes is refused too, in JSON like any other
	// refused path. With a character that should have been escaped, such as
	// a brace, the raw path is not a valid encoding, so EscapedPath is
	// rebuilt from the decoded path and has /api while the raw path does
	// not: StripPrefix would answer that in plain text.
	_, want := netDo(t, ts, http.MethodGet, "/api/v1/nothing-here")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{
		"/%61pi/v1/{x}",
		"/%61pi/v1/state{",
		"/%61pi/v1/|",
		"/a%70i/v1/^x",
		"/api/v%31/{x}",
		"/api/%76" + "1/sta\"te",
		"/%61pi/v1/state",
		"/a%70i/v1/state",
		"/ap%69/v1/state",
		"/api/%76" + "1/state",
		"/api/v%31/state",
		"/%61pi/v1",
		"/%61pi/v1/stop",
		"/%41PI/v1/state",
		"/%2Fapi/v1/state",
	} {
		req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.URL.Opaque = p
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("GET %s = %d %q %s, want 404 application/problem+json", p, resp.StatusCode, resp.Header.Get("Content-Type"), data)
			continue
		}
		var got, ref map[string]any
		if json.Unmarshal(data, &got) != nil || json.Unmarshal([]byte(want), &ref) != nil {
			t.Errorf("GET %s answered %s, which is not JSON", p, data)
			continue
		}
		if title, _ := got["title"].(string); !strings.HasPrefix(title, "no such route: ") {
			t.Errorf("GET %s: title is %q, want no such route", p, got["title"])
		}
		for _, k := range []string{"code", "status", "type"} {
			if !reflect.DeepEqual(got[k], ref[k]) {
				t.Errorf("GET %s: %s is %v, want %v as for an unknown path", p, k, got[k], ref[k])
			}
		}
	}
}

func TestNetworkEventsCountAsClients(t *testing.T) {
	s, ts := serveNetwork(t, newActionFake())
	before := s.hub.Clients()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/events = %d, want 200", resp.StatusCode)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return s.hub.Clients() == before+1 }, "the network stream to count as a client")
	cancel()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return s.hub.Clients() == before }, "the network stream to stop counting once cancelled")
}

// Every operation served by the page host is on the network: the page host
// serves nothing else.
func TestPagePathsAreMarkedNetwork(t *testing.T) {
	n := 0
	for path, item := range loadSchema(t).Paths.Map() {
		for method, op := range item.Operations() {
			if page, _ := op.Extensions["x-nearhost-page"].(bool); !page {
				continue
			}
			n++
			if net, _ := op.Extensions["x-nearhost-network"].(bool); !net {
				t.Errorf("%s %s is x-nearhost-page but not x-nearhost-network", strings.ToUpper(method), path)
			}
		}
	}
	if n == 0 {
		t.Error("no operation is marked x-nearhost-page")
	}
}
