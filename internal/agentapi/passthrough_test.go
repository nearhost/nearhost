package agentapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePasser records what it opens and closes. A port in taken is not free,
// and Open forwards it to local instead, when set. With block set, Open
// sends on started and waits for block to close, then records whether its
// context was cancelled meanwhile.
type fakePasser struct {
	mu       sync.Mutex
	live     map[string]Passthrough
	opens    []Passthrough
	closes   []string
	taken    map[int]bool
	local    int
	err      error
	block    chan struct{}
	started  chan struct{}
	openCtx  []error // ctx.Err() as each blocked Open returned
	closeCtx []error // ctx.Err() as each Close was called
}

func (f *fakePasser) Open(ctx context.Context, p Passthrough) (int, error) {
	f.mu.Lock()
	block, started := f.block, f.started
	f.mu.Unlock()
	if block != nil {
		started <- struct{}{}
		<-block
		f.mu.Lock()
		f.openCtx = append(f.openCtx, ctx.Err())
		f.mu.Unlock()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	p.Local = p.Remote
	if f.taken[p.Remote] && f.local != 0 {
		p.Local = f.local
	}
	f.live[p.ID] = p
	f.opens = append(f.opens, p)
	return p.Local, nil
}

func (f *fakePasser) Close(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCtx = append(f.closeCtx, ctx.Err())
	delete(f.live, id)
	f.closes = append(f.closes, id)
	return nil
}

func (f *fakePasser) List(origin string) []Passthrough {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Passthrough
	for _, p := range f.live {
		if p.Origin == origin {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakePasser) Free(port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.taken[port]
}

// blocking makes Open wait until the returned func is called.
func (f *fakePasser) blocking() (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block, f.started = make(chan struct{}), make(chan struct{}, 4)
	block := f.block
	return sync.OnceFunc(func() { close(block) })
}

func (f *fakePasser) ctxErrs() (open, closes []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.openCtx...), append([]error(nil), f.closeCtx...)
}

func (f *fakePasser) drop(id string) {
	f.mu.Lock()
	delete(f.live, id)
	f.mu.Unlock()
}

func (f *fakePasser) opened() []Passthrough {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Passthrough(nil), f.opens...)
}

func (f *fakePasser) closed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closes...)
}

// fakeGrants keeps one token per server and requester.
type fakeGrants struct {
	mu     sync.Mutex
	tokens map[[2]string]string
	allows [][2]string
	err    error
}

func (f *fakeGrants) Allow(server, requester string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allows = append(f.allows, [2]string{server, requester})
	tok := fmt.Sprintf("tok-%d", len(f.allows))
	f.tokens[[2]string{server, requester}] = tok
	return tok, nil
}

func (f *fakeGrants) Check(server, requester, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	return token != "" && f.tokens[[2]string{server, requester}] == token, nil
}

func (f *fakeGrants) allowed() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.allows...)
}

// apollo runs a project that forwards its loopback port 5173.
var apollo = []Project{
	{Name: "shop", Path: "/srv/shop", URL: "https://shop.test", State: "running", Web: 8080,
		Routes: []Route{
			{Port: 8080, Role: "web", Applied: true},
			{Port: 5173, Role: "loopback", Applied: true},
			{Port: 5174, Role: "loopback", Applied: false},
		}},
}

type passEnv struct {
	s      *Server
	r      *fakeReviewer
	p      *fakePasser
	g      *fakeGrants
	c      *clock
	local  string
	remote string // apollo's socket
	other  string // mini's socket
}

func newPassServer(t *testing.T, review bool) *passEnv {
	t.Helper()
	e := &passEnv{
		r: &fakeReviewer{},
		p: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}},
		g: &fakeGrants{tokens: map[[2]string]string{}},
		c: &clock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)},
	}
	e.s = New(Options{
		ID: "laptop-id", Laptop: "dev@laptop", Source: fakeSource{"apollo": apollo, "mini": mini},
		Passer: e.p, Grants: e.g, Now: e.c.Now,
	})
	if review {
		e.s.SetReviewer(e.r)
	}
	dir := shortDir(t)
	e.local, e.remote, e.other = filepath.Join(dir, "local.sock"), filepath.Join(dir, "apollo.sock"), filepath.Join(dir, "mini.sock")
	for origin, sock := range map[string]string{"local": e.local, "apollo": e.remote, "mini": e.other} {
		if err := e.s.Listen(origin, sock); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(e.s.Close)
	return e
}

// ask posts a passthrough request and returns its status and reply.
func (e *passEnv) ask(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return call(t, e.remote, "POST", "/v1/passthroughs", body)
}

// status calls the API and returns only the status, for replies with no body.
func status(t *testing.T, sock, method, path string) int {
	t.Helper()
	req, err := http.NewRequest(method, "http://nearhost"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client(sock).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestHelloAdvertisesPassthrough(t *testing.T) {
	e := newPassServer(t, true)
	_, out := call(t, e.remote, "GET", "/v1", "")
	caps, _ := out["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "passthrough" || out["id"] != "laptop-id" {
		t.Fatalf("with a Passer: %v", out)
	}
	_, _, _, _, remote := newServer(t, true)
	_, out = call(t, remote, "GET", "/v1", "")
	if caps, _ := out["capabilities"].([]any); len(caps) != 0 {
		t.Fatalf("without a Passer: %v", out)
	}
	if _, ok := out["id"]; ok {
		t.Fatalf("without a Passer, an id: %v", out)
	}
}

func TestPassthroughValidation(t *testing.T) {
	e := newPassServer(t, true)
	for name, body := range map[string]string{
		"port 0":        `{"port": 0, "requester": "claude-code"}`,
		"port 65536":    `{"port": 65536, "requester": "claude-code"}`,
		"capitals":      `{"port": 3000, "requester": "Claude"}`,
		"long":          `{"port": 3000, "requester": "` + strings.Repeat("a", 33) + `"}`,
		"no requester":  `{"port": 3000}`,
		"long session":  `{"port": 3000, "requester": "claude-code", "session": "` + strings.Repeat("s", 129) + `"}`,
		"label newline": `{"port": 3000, "requester": "claude-code", "label": "two\nlines"}`,
	} {
		if st, out := e.ask(t, body); st != 400 || out["error"] == nil {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}
	if st, out := call(t, e.local, "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`); st != 400 || out["error"] == nil {
		t.Errorf("on local: %d %v", st, out)
	}
	if len(e.r.prompts()) != 0 || len(e.p.opened()) != 0 {
		t.Fatalf("a bad request went through: %v %v", e.r.prompts(), e.p.opened())
	}
}

func TestPassthroughPrompt(t *testing.T) {
	e := newPassServer(t, true)
	e.p.taken[3000] = true
	st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1", "label": "browser preview"}`)
	if st != 202 || out["id"] == nil || out["expires_at"] != "2026-10-27T12:10:00Z" {
		t.Fatalf("%d %v", st, out)
	}
	st, out2 := e.ask(t, `{"port": 3001, "requester": "agent"}`)
	if st != 202 {
		t.Fatalf("agent: %d %v", st, out2)
	}
	got := e.r.prompts()
	if len(got) != 2 {
		t.Fatalf("prompts: %+v", got)
	}
	want := PassRequest{ID: out["id"].(string), Origin: "apollo", Requester: "claude-code", Session: "s1", Label: "browser preview",
		Remote: 3000, LocalTaken: true, CanAlways: true, Expires: e.c.Now().Add(10 * time.Minute)}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
	if got[1].LocalTaken || got[1].CanAlways || got[1].Requester != RequesterUnknown {
		t.Fatalf("agent's prompt: %+v", got[1])
	}
}

func TestPassthroughYes(t *testing.T) {
	e := newPassServer(t, true)
	e.p.taken[3000], e.p.local = true, 49152
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1"}`)
	id := out["id"].(string)
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "pending" {
		t.Fatalf("before the answer: %v", out)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		if err := e.s.AnswerPassthrough(context.Background(), id, AnswerYes); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	_, out = call(t, e.remote, "GET", "/v1/passthroughs/"+id+"?wait=5", "")
	if time.Since(start) > 3*time.Second {
		t.Fatal("the long poll did not return early")
	}
	if out["state"] != "open" || out["url"] != "http://localhost:49152/" || out["local_port"] != float64(49152) {
		t.Fatalf("after yes: %v", out)
	}
	if _, ok := out["token"]; ok {
		t.Fatalf("a token after yes: %v", out)
	}
	opens := e.p.opened()
	if len(opens) != 1 || opens[0].ID != id || opens[0].Remote != 3000 || opens[0].Session != "s1" || opens[0].Origin != "apollo" {
		t.Fatalf("opened %+v", opens)
	}
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
		t.Fatal("a second answer was taken")
	}
	if err := e.s.AnswerPassthrough(context.Background(), "nope", AnswerYes); err == nil {
		t.Fatal("an unknown id was answered")
	}
	if len(e.r.notices()) != 0 {
		t.Fatal("a prompted open was announced as always allowed")
	}
}

func TestPassthroughAlways(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code"}`)
	id := out["id"].(string)
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerAlways); err != nil {
		t.Fatal(err)
	}
	_, out = call(t, e.remote, "GET", "/v1/passthroughs/"+id+"?wait=5", "")
	if out["state"] != "open" || out["token"] != "tok-1" || out["url"] != "http://localhost:3000/" {
		t.Fatalf("first read: %v", out)
	}
	_, out = call(t, e.remote, "GET", "/v1/passthroughs/"+id, "")
	if _, ok := out["token"]; ok || out["state"] != "open" {
		t.Fatalf("second read: %v", out)
	}
	if got := e.g.allowed(); len(got) != 1 || got[0] != [2]string{"apollo", "claude-code"} {
		t.Fatalf("allowed %v", got)
	}
}

func TestPassthroughAlwaysRefusedForAgent(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "agent"}`)
	id := out["id"].(string)
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerAlways); err == nil {
		t.Fatal("always allow was taken for agent")
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "pending" {
		t.Fatalf("after the refusal: %v", out)
	}
	if len(e.g.allowed()) != 0 || len(e.p.opened()) != 0 {
		t.Fatal("agent got a grant or a forward")
	}
}

func TestPassthroughNo(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code"}`)
	id := out["id"].(string)
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerNo); err != nil {
		t.Fatal(err)
	}
	_, out = call(t, e.remote, "GET", "/v1/passthroughs/"+id+"?wait=5", "")
	if _, ok := out["reason"]; out["state"] != "declined" || ok {
		t.Fatalf("a user's no has a reason: %v", out)
	}
	if len(e.p.opened()) != 0 {
		t.Fatal("no opened a forward")
	}
}

func TestPassthroughNoOnAnEndedRequestIsANoOp(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code"}`)
	id := out["id"].(string)
	e.c.add(10 * time.Minute)
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerNo); err != nil {
		t.Fatalf("no on an expired request: %v", err)
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "expired" {
		t.Fatalf("no changed an expired request: %v", out)
	}
	if err := e.s.AnswerPassthrough(context.Background(), "nope", AnswerNo); err != nil {
		t.Fatalf("no on an unknown request: %v", err)
	}
	err := e.s.AnswerPassthrough(context.Background(), id, AnswerYes)
	if !errors.Is(err, ErrPassEnded) || !strings.Contains(err.Error(), "has ended: expired") {
		t.Fatalf("yes on an expired request: %v", err)
	}
	if err := e.s.AnswerPassthrough(context.Background(), "nope", AnswerYes); !errors.Is(err, ErrPassEnded) {
		t.Fatalf("yes on an unknown request: %v", err)
	}
}

func TestPassthroughExpires(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code"}`)
	id := out["id"].(string)
	e.c.add(10 * time.Minute)
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "expired" {
		t.Fatalf("%v", out)
	}
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
		t.Fatal("an expired prompt was answered")
	}
}

func TestPassthroughOpenFails(t *testing.T) {
	e := newPassServer(t, true)
	e.p.err = errors.New("ssh: the forward failed")
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code"}`)
	id := out["id"].(string)
	err := e.s.AnswerPassthrough(context.Background(), id, AnswerYes)
	if err == nil || err.Error() != "ssh: the forward failed" || !errors.Is(err, ErrPassEnded) || !errors.Is(err, e.p.err) {
		t.Fatalf("got %v", err)
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "declined" || out["reason"] != "ssh: the forward failed" {
		t.Fatalf("%v", out)
	}
}

func TestPassthroughToken(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1", "token": "good"}`)
	if st != 201 || out["state"] != "open" || out["url"] != "http://localhost:3000/" || out["local_port"] != float64(3000) || out["id"] == nil {
		t.Fatalf("a valid token: %d %v", st, out)
	}
	if len(e.r.prompts()) != 0 {
		t.Fatal("a valid token was prompted")
	}
	if n := e.r.notices(); len(n) != 1 || n[0].Remote != 3000 || n[0].Local != 3000 || n[0].Requester != "claude-code" {
		t.Fatalf("Opened: %+v", n)
	}
	st, out = e.ask(t, `{"port": 3001, "requester": "claude-code", "token": "bad"}`)
	if st != 401 || !strings.Contains(out["error"].(string), "revoked or never existed") {
		t.Fatalf("a wrong token: %d %v", st, out)
	}
	if len(e.r.prompts()) != 0 {
		t.Fatal("a wrong token was prompted")
	}
	if st, _ := e.ask(t, `{"port": 3002, "requester": "claude-code"}`); st != 202 || len(e.r.prompts()) != 1 {
		t.Fatalf("no token: %d %v", st, e.r.prompts())
	}
	e.g.err = errors.New("grants.json: permission denied")
	if st, out := e.ask(t, `{"port": 3003, "requester": "claude-code", "token": "good"}`); st != 500 || !strings.Contains(out["error"].(string), "permission denied") {
		t.Fatalf("a failed check: %d %v", st, out)
	}
}

func TestPassthroughHeadless(t *testing.T) {
	e := newPassServer(t, false)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	if st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "token": "good"}`); st != 201 || out["state"] != "open" {
		t.Fatalf("a valid token: %d %v", st, out)
	}
	st, out := e.ask(t, `{"port": 3001, "requester": "claude-code"}`)
	if st != 409 || !strings.Contains(out["error"].(string), "open nearhost on dev@laptop to approve this") {
		t.Fatalf("no token: %d %v", st, out)
	}
}

func TestPassthroughRepeats(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	_, first := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1", "token": "good"}`)
	st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1"}`)
	if st != 200 || out["id"] != first["id"] || out["state"] != "open" || out["url"] != "http://localhost:3000/" {
		t.Fatalf("an open repeat: %d %v", st, out)
	}
	if len(e.p.opened()) != 1 || len(e.r.prompts()) != 0 {
		t.Fatal("a repeat opened again or prompted")
	}

	_, pending := e.ask(t, `{"port": 4000, "requester": "claude-code", "session": "s1"}`)
	st, out = e.ask(t, `{"port": 4000, "requester": "claude-code", "session": "s1"}`)
	if st != 202 || out["id"] != pending["id"] {
		t.Fatalf("a pending repeat: %d %v", st, out)
	}
	if len(e.r.prompts()) != 1 {
		t.Fatalf("prompted %d times", len(e.r.prompts()))
	}

	for _, port := range []string{"3000", "4000"} {
		st, out := e.ask(t, `{"port": `+port+`, "requester": "claude-code", "session": "s2"}`)
		if msg, _ := out["error"].(string); st != 409 || !strings.Contains(msg, "claude-code") || !strings.Contains(msg, "s1") {
			t.Errorf("port %s from another session: %d %v", port, st, out)
		}
	}
	if st, _ := e.ask(t, `{"port": 3000, "requester": "codex", "session": "s1"}`); st != 409 {
		t.Errorf("another requester: %d", st)
	}
	// Another server's port of the same number is its own.
	if st, _ := call(t, e.other, "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "session": "s2"}`); st != 202 {
		t.Errorf("on mini: %d", st)
	}
}

func TestPassthroughCoveredByProject(t *testing.T) {
	e := newPassServer(t, true)
	st, out := e.ask(t, `{"port": 5173, "requester": "claude-code"}`)
	if st != 200 || out["state"] != "open" || out["via"] != "project" || out["url"] != "http://localhost:5173/" {
		t.Fatalf("%d %v", st, out)
	}
	if len(e.r.prompts()) != 0 || len(e.p.opened()) != 0 {
		t.Fatal("a project's port was prompted or opened")
	}
	// A route that is not applied does not reach the laptop.
	if st, _ := e.ask(t, `{"port": 5174, "requester": "claude-code"}`); st != 202 {
		t.Fatalf("an unapplied route: %d", st)
	}
}

func TestPassthroughNotConnected(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	e.p.err = fmt.Errorf("apollo: %w", ErrNotConnected)
	st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "token": "good"}`)
	if msg, _ := out["error"].(string); st != 409 || !strings.Contains(msg, "apollo") || !strings.Contains(msg, "press c") {
		t.Fatalf("%d %v", st, out)
	}
	// Nothing holds the port afterwards.
	e.p.err = nil
	if st, _ := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "other", "token": "good"}`); st != 201 {
		t.Fatalf("asked again: %d", st)
	}
}

func TestPassthroughLimit(t *testing.T) {
	e := newPassServer(t, true)
	for i := range 3 {
		if st, out := call(t, e.remote, "POST", "/v1/proposals", fmt.Sprintf(`{"path": "/srv/app%d"}`, i)); st != 202 {
			t.Fatalf("proposal %d: %d %v", i, st, out)
		}
	}
	for i := range 3 {
		if st, out := e.ask(t, fmt.Sprintf(`{"port": %d, "requester": "claude-code"}`, 3000+i)); st != 202 {
			t.Fatalf("prompt %d: %d %v", i, st, out)
		}
	}
	if st, out := e.ask(t, `{"port": 3003, "requester": "claude-code"}`); st != 429 || out["error"] == nil {
		t.Fatalf("a fourth: %d %v", st, out)
	}
}

func TestPassthroughClosed(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code"}`)
	id := out["id"].(string)
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerYes); err != nil {
		t.Fatal(err)
	}
	e.p.drop(id)
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "closed" {
		t.Fatalf("%v", out)
	}
	// The port is free to ask for again, from any session.
	if st, _ := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s2"}`); st != 202 {
		t.Fatalf("asked again: %d", st)
	}
}

func TestPassthroughList(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1", "token": "good"}`)
	e.ask(t, `{"port": 3001, "requester": "claude-code", "session": "s2"}`)
	call(t, e.other, "POST", "/v1/passthroughs", `{"port": 3002, "requester": "claude-code", "session": "s1"}`)

	_, out := call(t, e.remote, "GET", "/v1/passthroughs", "")
	all := out["passthroughs"].([]any)
	if len(all) != 2 {
		t.Fatalf("listed %v", all)
	}
	states := map[float64]any{}
	for _, p := range all {
		m := p.(map[string]any)
		states[m["port"].(float64)] = m["state"]
	}
	if states[3000] != "open" || states[3001] != "pending" {
		t.Fatalf("listed %v", all)
	}
	_, out = call(t, e.remote, "GET", "/v1/passthroughs?session=s2", "")
	if got := out["passthroughs"].([]any); len(got) != 1 || got[0].(map[string]any)["port"] != float64(3001) {
		t.Fatalf("s2's: %v", got)
	}
	_, out = call(t, e.remote, "GET", "/v1/passthroughs?session=none", "")
	if got := out["passthroughs"].([]any); len(got) != 0 {
		t.Fatalf("none's: %v", got)
	}
}

func TestPassthroughDelete(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	_, one := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s1", "token": "good"}`)
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs/"+one["id"].(string)); st != 204 {
		t.Fatalf("delete one: %d", st)
	}
	if got := e.p.closed(); len(got) != 1 || got[0] != one["id"] {
		t.Fatalf("closed %v", got)
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+one["id"].(string), ""); out["state"] != "closed" {
		t.Fatalf("after delete: %v", out)
	}

	_, a := e.ask(t, `{"port": 3001, "requester": "claude-code", "session": "s", "token": "good"}`)
	_, b := e.ask(t, `{"port": 3002, "requester": "claude-code", "session": "t", "token": "good"}`)
	call(t, e.other, "POST", "/v1/passthroughs", `{"port": 3003, "requester": "claude-code", "session": "s", "token": "good"}`)
	_, pend := e.ask(t, `{"port": 3004, "requester": "claude-code", "session": "s"}`)
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs?session=s"); st != 204 {
		t.Fatalf("delete a session: %d", st)
	}
	closed := e.p.closed()
	if len(closed) != 2 || closed[1] != a["id"] {
		t.Fatalf("closed %v; want only %v after the first", closed, a["id"])
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+b["id"].(string), ""); out["state"] != "open" {
		t.Fatalf("another session's: %v", out)
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+pend["id"].(string), ""); out["state"] != "closed" {
		t.Fatalf("the session's prompt: %v", out)
	}
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs?session=nobody"); st != 204 {
		t.Fatalf("a session with none: %d", st)
	}
}

// waitStarted waits for a blocked Open to start.
func (e *passEnv) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-e.p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Open did not start")
	}
}

// The session-end hook closes the session while a token open is in flight,
// as the agent's client is torn down: the forward that opens must still be
// closed, with a context the hang-up did not cancel.
func TestPassthroughSessionClosedDuringATokenOpen(t *testing.T) {
	for _, hangUp := range []bool{false, true} {
		t.Run(fmt.Sprintf("hangUp=%v", hangUp), func(t *testing.T) {
			e := newPassServer(t, true)
			e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
			release := e.p.blocking()
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://nearhost/v1/passthroughs",
				strings.NewReader(`{"port": 3000, "requester": "claude-code", "session": "s", "token": "good"}`))
			type result struct {
				status int
				body   string
				err    error
			}
			got := make(chan result, 1)
			go func() {
				resp, err := client(e.remote).Do(req)
				if err != nil {
					got <- result{err: err}
					return
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				got <- result{status: resp.StatusCode, body: string(b)}
			}()
			e.waitStarted(t)
			if st := status(t, e.remote, "DELETE", "/v1/passthroughs?session=s"); st != 204 {
				t.Fatalf("delete the session: %d", st)
			}
			if hangUp {
				cancel()
				<-got
				time.Sleep(200 * time.Millisecond) // for the server to see the hang-up
			}
			release()
			if !hangUp {
				res := <-got
				if res.status != 409 || strings.Contains(res.body, `"open"`) {
					t.Fatalf("the POST: %d %s", res.status, res.body)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(e.p.closed()) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if closed := e.p.closed(); len(closed) != 1 {
				t.Fatalf("the forward was not closed: %v", closed)
			}
			if _, closes := e.p.ctxErrs(); len(closes) != 1 || closes[0] != nil {
				t.Fatalf("Close got a cancelled context: %v", closes)
			}
			if live := e.p.List("apollo"); len(live) != 0 {
				t.Fatalf("a forward is left: %v", live)
			}
		})
	}
}

func TestPassthroughTokenOpenOutlivesTheClient(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	release := e.p.blocking()
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://nearhost/v1/passthroughs",
		strings.NewReader(`{"port": 3000, "requester": "claude-code", "session": "s", "token": "good"}`))
	hungUp := make(chan struct{})
	go func() {
		defer close(hungUp)
		if resp, err := client(e.remote).Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	e.waitStarted(t)
	cancel()
	<-hungUp
	time.Sleep(200 * time.Millisecond) // for the server to see the hang-up
	release()

	// The agent asks again and finds it open.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s"}`)
		if st == 200 && out["state"] == "open" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("asked again: %d %v", st, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if opens, _ := e.p.ctxErrs(); len(opens) != 1 || opens[0] != nil {
		t.Fatalf("the hang-up cancelled Open: %v", opens)
	}
}

func TestPassthroughAlwaysWhileTheSessionCloses(t *testing.T) {
	e := newPassServer(t, true)
	_, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "session": "s"}`)
	id := out["id"].(string)
	release := e.p.blocking()
	defer release()
	answered := make(chan error, 1)
	go func() { answered <- e.s.AnswerPassthrough(context.Background(), id, AnswerAlways) }()
	e.waitStarted(t)
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs?session=s"); st != 204 {
		t.Fatalf("delete the session: %d", st)
	}
	release()
	if err := <-answered; err == nil || !strings.Contains(err.Error(), "session was closed") {
		t.Fatalf("answer: %v", err)
	}
	if got := e.g.allowed(); len(got) != 0 {
		t.Fatalf("a grant was saved: %v", got)
	}
	if closed := e.p.closed(); len(closed) != 1 || closed[0] != id {
		t.Fatalf("the forward was not closed: %v", closed)
	}
	if _, closes := e.p.ctxErrs(); len(closes) != 1 || closes[0] != nil {
		t.Fatalf("Close got a cancelled context: %v", closes)
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+id, ""); out["state"] != "closed" {
		t.Fatalf("after: %v", out)
	}
}
