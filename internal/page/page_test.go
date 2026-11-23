package page

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// testToken is the form token the tests' pages carry.
const testToken = "tok123"

func init() {
	csrfToken = func() (string, error) { return testToken, nil }
}

// now is the time the tests' pages are rendered at.
var now = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// fakeCore is a core with a fixed status and state, which records the
// answers it is given and returns err for each.
type fakeCore struct {
	mu     sync.Mutex
	status control.Status
	state  control.State
	err    error
	calls  []string
}

func (f *fakeCore) Status() control.Status { return f.status }
func (f *fakeCore) State() control.State   { return f.state }

func (f *fakeCore) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeCore) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeCore) AnswerPassthrough(ctx context.Context, id, answer string) error {
	if ctx == nil {
		panic("no context")
	}
	f.record("AnswerPassthrough " + id + " " + answer)
	return f.err
}

func (f *fakeCore) AnswerDocument(ctx context.Context, id, answer string) error {
	f.record("AnswerDocument " + id + " " + answer)
	return f.err
}

func (f *fakeCore) DeclineProposal(id, reason string) {
	f.record("DeclineProposal " + id + " " + reason)
}

func (f *fakeCore) AcceptChange(project, sha string) error {
	f.record("AcceptChange " + project + " " + sha)
	return f.err
}

func (f *fakeCore) DeclineChange(project, sha string) {
	f.record("DeclineChange " + project + " " + sha)
}

// newCore is a core started an hour before now by the login service, with
// approvals pending.
func newCore(approvals ...control.Approval) *fakeCore {
	info := control.Info{API: 1, Version: "0.15.0", Started: now.Add(-3 * time.Minute), StartedBy: control.StartedService}
	return &fakeCore{
		status: control.Status{Info: info},
		state: control.State{
			Info:      info,
			WebPort:   6327,
			Projects:  map[string]engine.ProjectStatus{},
			Servers:   map[string]control.ServerState{},
			Approvals: approvals,
		},
	}
}

func newPage(c Core) http.Handler {
	return New(Options{Core: c, Now: func() time.Time { return now }})
}

// do sends a request for path to h, with form as the body of a POST, and
// returns the response.
func do(h http.Handler, method, path string, form url.Values, header ...string) *http.Response {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

// bodyOf reads resp's body.
func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatusLists(t *testing.T) {
	c := newCore()
	c.status.Servers = []control.ServerInfo{
		{Name: config.LocalServer, State: "connected"},
		{Name: "leto", State: "reconnecting", Message: "ssh: connection refused"},
	}
	c.state.Servers["leto"] = control.ServerState{Health: engine.ServerHealth{Health: transport.Health{Status: transport.StatusReconnecting, Failures: 2}}, Status: transport.StatusReconnecting, Message: "ssh: connection refused"}
	c.status.Projects = []control.ProjectInfo{
		{Name: "web", State: "running", URL: "https://web.test"},
		{Name: "api", State: "stopped", Message: "waiting for the hosts file: run nearhost to update it", URL: "https://api.test"},
	}
	c.state.Passthroughs = []engine.Passthrough{{
		PassthroughSpec: engine.PassthroughSpec{ID: "p9", Server: "leto", Requester: "claude-code", Label: "vite", Remote: 5173},
		Local:           5174, Opened: now.Add(-10 * time.Minute),
	}}
	resp := do(newPage(c), "GET", "/", nil)
	body := bodyOf(t, resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("GET /: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		"0.15.0", "3 min ago", "the login service",
		"leto", "reconnecting", "ssh: connection refused",
		`<a href="https://web.test">https://web.test</a>`, "running",
		"api", "waiting for the hosts file: run nearhost to update it",
		"Claude Code", "5173", "5174", "vite", "opened 10 min ago",
		`<script src="/page.js" defer></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the status page lacks %q:\n%s", want, body)
		}
	}
}

func TestStatusListsTrees(t *testing.T) {
	c := newCore()
	h := New(Options{Core: c, Now: func() time.Time { return now }, Trees: func() []proxy.HomeTree {
		return []proxy.HomeTree{{Server: "apollo", Root: "/srv/app", Docs: []proxy.HomeDoc{{Path: "plan.md", URL: "/d/abc123/plan.md"}}}}
	}})
	body := bodyOf(t, do(h, "GET", "/", nil))
	for _, want := range []string{"apollo", "/srv/app", `<a href="/d/abc123/plan.md">plan.md</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
}

func TestNothingWaits(t *testing.T) {
	body := bodyOf(t, do(newPage(newCore()), "GET", "/", nil))
	if !strings.Contains(body, "Nothing waits for your answer.") {
		t.Errorf("no approvals:\n%s", body)
	}
}

func TestStatusLinksApprovals(t *testing.T) {
	c := newCore(passApproval("p1", true))
	c.state.Projects["web"] = engine.ProjectStatus{Pending: &engine.Change{SHA256: "abc"}}
	body := bodyOf(t, do(newPage(c), "GET", "/", nil))
	for _, want := range []string{`href="/approve/p1"`, `href="/change/web"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Nothing waits for your answer.") {
		t.Error("says nothing waits while two do")
	}
}

func TestMainPart(t *testing.T) {
	c := newCore(passApproval("p1", true))
	for _, path := range []string{"/", "/approve/p1"} {
		resp := do(newPage(c), "GET", path, nil, "X-Nearhost-Part", "main")
		body := bodyOf(t, resp)
		if resp.StatusCode != 200 || strings.Contains(body, "<html") || strings.Contains(body, "<script") || strings.Contains(body, "<main") {
			t.Errorf("%s main part: %d\n%s", path, resp.StatusCode, body)
		}
		if !strings.Contains(body, "nearhost") {
			t.Errorf("%s main part has no content:\n%s", path, body)
		}
		if resp.Header.Get("X-Nearhost-Part") != "main" {
			t.Errorf("%s main part is not marked", path)
		}
	}
}

// A part for an approval that waits no longer is marked too, so page.js
// shows that it has ended; the whole page is not marked.
func TestMainPartMarker(t *testing.T) {
	h := newPage(newCore())
	if resp := do(h, "GET", "/approve/gone", nil, "X-Nearhost-Part", "main"); resp.StatusCode != 404 || resp.Header.Get("X-Nearhost-Part") != "main" {
		t.Errorf("the part of an ended request: %d, marker %q", resp.StatusCode, resp.Header.Get("X-Nearhost-Part"))
	}
	// The part carries the view's title too, escaped, so page.js can put
	// it on the tab: an ended request no longer reads as one that waits.
	if resp := do(h, "GET", "/approve/gone", nil, "X-Nearhost-Part", "main"); resp.Header.Get("X-Nearhost-Title") != "Nothing%20to%20answer" {
		t.Errorf("the part's title is %q", resp.Header.Get("X-Nearhost-Title"))
	}
	if resp := do(h, "GET", "/", nil); resp.Header.Get("X-Nearhost-Part") != "" {
		t.Error("the whole page is marked as a part")
	}
}

// Links to what waits are escaped as paths.
func TestStatusEscapesLinks(t *testing.T) {
	c := newCore(control.Approval{ID: "a/b?c", Pass: passApproval("a/b?c", false).Pass})
	c.state.Projects["my app"] = engine.ProjectStatus{Pending: &engine.Change{SHA256: "abc"}}
	body := bodyOf(t, do(newPage(c), "GET", "/", nil))
	for _, want := range []string{`href="/approve/a%2Fb%3Fc"`, `href="/change/my%20app"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
}

// A core whose start time is unknown says who started it, without an age.
func TestStatusWithoutStart(t *testing.T) {
	c := newCore()
	c.status.Info.Started = time.Time{}
	body := bodyOf(t, do(newPage(c), "GET", "/", nil))
	if !strings.Contains(body, "Nearhost was started by the login service.") || strings.Contains(body, "started ,") {
		t.Errorf("no start time:\n%s", body)
	}
}

func TestHeadHasNoBody(t *testing.T) {
	c := newCore(passApproval("p1", true))
	for _, path := range []string{"/", "/approve/p1", "/page.js", "/events"} {
		resp := do(newPage(c), "HEAD", path, nil)
		if body := bodyOf(t, resp); resp.StatusCode != 200 || body != "" {
			t.Errorf("HEAD %s: %d %q", path, resp.StatusCode, body)
		}
	}
}

func TestPageScript(t *testing.T) {
	resp := do(newPage(newCore()), "GET", "/page.js", nil)
	body := bodyOf(t, resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/javascript" || !strings.Contains(body, "EventSource") {
		t.Fatalf("GET /page.js: %d %s\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if n := strings.Count(body, "\n"); n >= 60 {
		t.Errorf("page.js has %d lines, want under 60", n)
	}
	if strings.Contains(body, "eval(") {
		t.Error("page.js calls eval")
	}
}

// The gate's headers stay as the gate set them.
func TestKeepsTheGatesHeaders(t *testing.T) {
	c := newCore(passApproval("p1", true))
	for _, path := range []string{"/", "/approve/p1", "/page.js"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", "csp")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "Cookie")
		newPage(c).ServeHTTP(w, r)
		h := w.Result().Header
		if h.Get("Content-Security-Policy") != "csp" || h.Get("Cache-Control") != "no-store" || h.Get("Vary") != "Cookie" {
			t.Errorf("%s: headers %v", path, h)
		}
	}
}

func TestTemplatesEscape(t *testing.T) {
	const evil = `<img src=x onerror=1>`
	pass := passApproval("p1", true)
	pass.Pass.Label = evil
	pass.Pass.Requester = evil
	doc := docApproval("d1")
	doc.Doc.Label, doc.Doc.Doc, doc.Doc.Root = evil, evil, evil
	prop := control.Approval{ID: "x", Proposal: &agentapi.Proposal{ID: "x", Origin: evil, Path: evil, Name: evil}}
	c := newCore(pass, doc, prop)
	c.state.Passthroughs = []engine.Passthrough{{PassthroughSpec: engine.PassthroughSpec{ID: "p9", Server: evil, Requester: evil, Label: evil, Remote: 1}}}
	c.status.Servers = []control.ServerInfo{{Name: evil, Message: evil}}
	c.status.Projects = []control.ProjectInfo{{Name: evil, Message: evil, URL: "https://web.test"}}
	c.state.Projects[evil] = engine.ProjectStatus{Pending: &engine.Change{SHA256: "abc", Items: []nearhostrc.Item{
		{Port: 80, Host: evil, Why: nearhostrc.WhyRealDomain},
	}}}
	h := New(Options{Core: c, Now: func() time.Time { return now }, Trees: func() []proxy.HomeTree {
		return []proxy.HomeTree{{Server: evil, Root: evil, Docs: []proxy.HomeDoc{{Path: evil, URL: "/d/x/y"}}}}
	}})
	for _, path := range []string{"/", "/approve/p1", "/approve/d1", "/approve/x", "/change/" + url.PathEscape(evil)} {
		body := bodyOf(t, do(h, "GET", path, nil))
		if strings.Contains(body, "<img") {
			t.Errorf("%s shows the label unescaped:\n%s", path, body)
		}
		if !strings.Contains(body, "&lt;img src=x onerror=1&gt;") {
			t.Errorf("%s does not show the label:\n%s", path, body)
		}
	}
}

// buttons lists the labels of the buttons in body.
func buttons(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<button[^>]*>([^<]*)</button>`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// eachFormHasToken reports whether every form in body carries the token.
func eachFormHasToken(body string) bool {
	forms := strings.Count(body, "<form")
	return forms > 0 && strings.Count(body, `<input type="hidden" name="csrf" value="`+testToken+`">`) == forms
}
