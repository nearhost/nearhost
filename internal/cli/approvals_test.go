package cli

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/page"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// fakeClock is a clock the test moves on, safe to read from the sweeper.
type fakeClock struct{ at atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.at.Store(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.at.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.at.Add(int64(d)) }

// published records what q publishes, as well as publishing it to its hub.
type published struct {
	mu   sync.Mutex
	envs []control.Envelope
}

func record(q *approvals) *published {
	p := &published{}
	next := q.publish
	q.publish = func(e control.Envelope) uint64 {
		p.mu.Lock()
		defer p.mu.Unlock()
		e.Seq = next(e)
		p.envs = append(p.envs, e)
		return e.Seq
	}
	return p
}

// all is what has been published so far.
func (p *published) all() []control.Envelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.envs)
}

// ended reports whether an "ended" for id has been published.
func (p *published) ended(id string) bool {
	return slices.ContainsFunc(p.all(), func(e control.Envelope) bool { return e.Kind == control.KindEnded && e.ID == id })
}

// newQueue is a queue on a hub of its own, recorded, and closed when the
// test ends.
func newQueue(t *testing.T, clock *fakeClock) (*approvals, *published, *control.Hub) {
	t.Helper()
	hub := control.NewHub()
	q := newApprovals(hub, clock.now)
	t.Cleanup(q.close)
	return q, record(q), hub
}

func TestApprovalsQueueAndStream(t *testing.T) {
	clock := newFakeClock()
	q, pub, hub := newQueue(t, clock)
	req := agentapi.PassRequest{ID: "p1", Origin: "mini", Requester: "claude-code", Remote: 5173, Expires: clock.now().Add(time.Minute)}
	q.ReviewPassthrough(req)
	q.Review(agentapi.Proposal{ID: "a1", Origin: "mini", Path: "/srv/app", Expires: clock.now().Add(time.Minute)})
	q.ReviewDocument(agentapi.DocRequest{ID: "d1", Origin: "mini", Root: "/srv/app", Doc: "plan.md", Expires: clock.now().Add(time.Minute)})

	envs := pub.all()
	if len(envs) != 3 || hub.Seq() != 3 {
		t.Fatalf("published %+v, hub at %d", envs, hub.Seq())
	}
	first := envs[0]
	if first.Kind != control.KindApproval || first.Approval == nil || first.Approval.ID != "p1" || first.Approval.Pass == nil || *first.Approval.Pass != req {
		t.Errorf("first envelope %+v", first)
	}
	if a := envs[1].Approval; a == nil || a.ID != "a1" || a.Proposal == nil || a.Proposal.Path != "/srv/app" {
		t.Errorf("second envelope %+v", envs[1])
	}
	if a := envs[2].Approval; a == nil || a.ID != "d1" || a.Doc == nil || a.Doc.Doc != "plan.md" {
		t.Errorf("third envelope %+v", envs[2])
	}
	var ids []string
	for _, a := range q.Pending() {
		ids = append(ids, a.ID)
	}
	if want := []string{"p1", "a1", "d1"}; !slices.Equal(ids, want) {
		t.Errorf("pending %v, want %v", ids, want)
	}
	if !q.take("a1") || q.take("a1") {
		t.Error("take did not take a1 once")
	}
	if !pub.ended("a1") || len(q.Pending()) != 2 {
		t.Errorf("after take: published %+v, pending %+v", pub.all(), q.Pending())
	}
}

func TestExpiredApprovalsDrop(t *testing.T) {
	clock := newFakeClock()
	q, pub, _ := newQueue(t, clock)
	q.ReviewPassthrough(agentapi.PassRequest{ID: "p1", Expires: clock.now().Add(time.Minute)})
	q.ReviewPassthrough(agentapi.PassRequest{ID: "p2", Expires: clock.now().Add(time.Hour)})
	clock.advance(time.Minute + time.Second)
	got := q.Pending()
	if len(got) != 1 || got[0].ID != "p2" {
		t.Fatalf("pending %+v, want p2 alone", got)
	}
	if !pub.ended("p1") || pub.ended("p2") {
		t.Errorf("published %+v", pub.all())
	}
	if q.take("p1") {
		t.Error("took an approval that had expired")
	}
}

// An approval that expires while nobody asks for the pending ones still
// ends, once the sweeper next looks.
func TestExpiredApprovalsEndWithoutAsking(t *testing.T) {
	before := approvalSweep
	approvalSweep = 10 * time.Millisecond
	t.Cleanup(func() { approvalSweep = before })
	clock := newFakeClock()
	q, pub, _ := newQueue(t, clock)
	q.ReviewDocument(agentapi.DocRequest{ID: "d1", Expires: clock.now().Add(time.Minute)})
	clock.advance(2 * time.Minute)
	testutil.WaitUntil(t, 5*time.Second, func() bool { return pub.ended("d1") }, "the expired request to end")
	// The sweeper stops once nothing is pending, and close waits for it.
	q.close()
}

func TestNoticesAreNotPending(t *testing.T) {
	clock := newFakeClock()
	q, pub, _ := newQueue(t, clock)
	p := agentapi.Passthrough{ID: "o1", Origin: "mini", Requester: "claude-code", Remote: 5173, Local: 5173}
	q.Opened(p)
	q.Shown("mini", "claude-code", "plan.md")
	envs := pub.all()
	if len(envs) != 2 {
		t.Fatalf("published %+v", envs)
	}
	if envs[0].Kind != control.KindOpened || envs[0].Opened == nil || *envs[0].Opened != p {
		t.Errorf("opened %+v", envs[0])
	}
	want := control.Shown{Origin: "mini", Requester: "claude-code", Doc: "plan.md"}
	if envs[1].Kind != control.KindShown || envs[1].Shown == nil || *envs[1].Shown != want {
		t.Errorf("shown %+v", envs[1])
	}
	if got := q.Pending(); len(got) != 0 {
		t.Errorf("pending %+v", got)
	}
}

// An answer through the core takes the request off the queue and ends it on
// the stream; a second answer finds it ended.
func TestAnsweredApprovalEnds(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	f, _ := opts.Agent("mini")
	clock := newFakeClock()
	q, pub, _ := newQueue(t, clock)
	q.now = time.Now // the agent API's requests expire by the real clock
	a.review(q)
	o := &coreOps{agents: a, approvals: q}
	status, out := postPass(t, f, "")
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)
	if got := q.Pending(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("pending %+v, want %s", got, id)
	}
	if err := o.AnswerPassthrough(context.Background(), id, "no"); err != nil {
		t.Fatal(err)
	}
	if len(q.Pending()) != 0 || !pub.ended(id) {
		t.Errorf("after the answer: pending %+v, published %+v", q.Pending(), pub.all())
	}
	err := o.AnswerPassthrough(context.Background(), id, "no")
	if !errors.Is(err, control.ErrRequestEnded) {
		t.Errorf("the second answer: %v", err)
	}
}

// An answer the agent API refuses leaves its request waiting, so a client
// can still answer it: the queue keeps it and publishes no "ended". An
// answer it takes ends the request.
func TestRefusedAnswerStaysPending(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	f, _ := opts.Agent("mini")
	q, pub, _ := newQueue(t, newFakeClock())
	q.now = time.Now // the agent API's requests expire by the real clock
	a.review(q)
	o := &coreOps{agents: a, approvals: q}
	// An agent that does not say which tool it is can't be always allowed.
	status, out := postAgentJSON(t, f, "/v1/passthroughs", `{"port": 5173, "requester": "`+agentapi.RequesterUnknown+`", "session": "s1"}`)
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)
	err := o.AnswerPassthrough(context.Background(), id, "always")
	if err == nil || errors.Is(err, control.ErrRequestEnded) {
		t.Fatalf("always for an unnamed agent: %v", err)
	}
	if got := q.Pending(); len(got) != 1 || got[0].ID != id || pub.ended(id) {
		t.Fatalf("after the refusal: pending %+v, published %+v", got, pub.all())
	}
	if err := o.AnswerPassthrough(context.Background(), id, "no"); err != nil {
		t.Fatal(err)
	}
	if len(q.Pending()) != 0 || !pub.ended(id) {
		t.Errorf("after the answer: pending %+v, published %+v", q.Pending(), pub.all())
	}
}

// agentUp is an up that serves coding agents, with grants, on home's socket.
// setup, when given, changes the run before it starts.
func agentUp(t *testing.T, setup ...func(*upRun)) (u *upRun, sock string) {
	t.Helper()
	home := agentHome(t)
	u = newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	for _, f := range setup {
		f(u)
	}
	adjust := u.adjust
	u.adjust = func(b *upBackend) {
		b.agents = withGrants(fakeAgents(home), home)
		if adjust != nil {
			adjust(b)
		}
	}
	u.start()
	u.waitFor("Serving until interrupted")
	return u, agentSocket(home, "k3f9q2")
}

// askToShow asks the agent API on sock to show a document of a new
// repository, with no token, as a request that needs the user's answer. A
// passthrough would do as well, but only a server's socket takes one, and
// the up of these tests has no server.
func askToShow(t *testing.T, sock string) (int, map[string]any) {
	t.Helper()
	repo := gitRepo(t)
	return postDoc(t, sock, filepath.Join(repo, "docs", "p.md"), repo, "")
}

// interfaceOn is what the agent API on sock says of the interface.
func interfaceOn(t *testing.T, sock string) any { return getJSON(t, sock, "/v1")["interface"] }

// A client that attaches finds in the state the requests that came before.
func TestStateCarriesPending(t *testing.T) {
	u, sock := agentUp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	envs, _, err := u.control().Events(ctx, u.hubSeq(t))
	if err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return interfaceOn(t, sock) == true }, "the agent API to say a client reviews")
	status, out := askToShow(t, sock)
	if status != 202 {
		t.Fatalf("document request: %d %v", status, out)
	}
	id := out["id"].(string)
	timeout := time.After(10 * time.Second)
	for streamed := false; !streamed; {
		select {
		case env := <-envs:
			streamed = env.Kind == control.KindApproval && env.Approval.ID == id
		case <-timeout:
			t.Fatal("the request was not streamed")
		}
	}
	// Client B attaches after the request came, as a client does that
	// mirrors the state and follows the stream from its number.
	b := u.control()
	st, err := b.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Events(ctx, st.Seq); err != nil {
		t.Fatal(err)
	}
	if len(st.Approvals) != 1 || st.Approvals[0].ID != id || st.Approvals[0].Doc == nil || st.Approvals[0].Doc.Doc != "docs/p.md" {
		t.Errorf("approvals %+v, want %s", st.Approvals, id)
	}
	cancel()
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// A No to one passthrough ends the origin's other prompts in the agent API,
// which tells the queue of each through Ended: no client is left showing
// one that has ended.
func TestNoClearsTheOriginsPrompts(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	f, _ := opts.Agent("mini")
	q, pub, _ := newQueue(t, newFakeClock())
	q.now = time.Now // the agent API's requests expire by the real clock
	a.review(q)
	o := &coreOps{agents: a, approvals: q}
	var ids []string
	for _, port := range []string{"5173", "5174"} {
		status, out := postAgentJSON(t, f, "/v1/passthroughs", `{"port": `+port+`, "requester": "claude-code", "session": "s1", "label": "vite"}`)
		if status != 202 {
			t.Fatalf("passthrough request for %s: %d %v", port, status, out)
		}
		ids = append(ids, out["id"].(string))
	}
	if got := q.Pending(); len(got) != 2 {
		t.Fatalf("pending %+v, want both", got)
	}
	if err := o.AnswerPassthrough(context.Background(), ids[0], "no"); err != nil {
		t.Fatal(err)
	}
	if got := q.Pending(); len(got) != 0 || !pub.ended(ids[0]) || !pub.ended(ids[1]) {
		t.Errorf("after the No: pending %+v, published %+v", got, pub.all())
	}
	// Each request ends once on the stream.
	for _, id := range ids {
		n := 0
		for _, e := range pub.all() {
			if e.Kind == control.KindEnded && e.ID == id {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s ended %d times, want once", id, n)
		}
	}
}

// Ended takes a pending request off the queue and ends it on the stream;
// for one not pending it publishes nothing.
func TestQueueFollowsEnded(t *testing.T) {
	clock := newFakeClock()
	q, pub, _ := newQueue(t, clock)
	q.ReviewPassthrough(agentapi.PassRequest{ID: "p1", Expires: clock.now().Add(time.Minute)})
	q.Ended("p1")
	if got := q.Pending(); len(got) != 0 || !pub.ended("p1") {
		t.Errorf("after Ended: pending %+v, published %+v", got, pub.all())
	}
	before := len(pub.all())
	q.Ended("p1")
	q.Ended("nope")
	if after := pub.all(); len(after) != before {
		t.Errorf("Ended of a request not pending published %+v", after[before:])
	}
}

// A request the agent withdraws leaves the queue at once: a client stops
// showing it, rather than when it expires.
func TestWithdrawnPassthroughEndsInTheQueue(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	f, _ := opts.Agent("mini")
	q, pub, _ := newQueue(t, newFakeClock())
	q.now = time.Now // the agent API's requests expire by the real clock
	a.review(q)
	status, out := postPass(t, f, "")
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)
	if status := deleteAgent(t, f, "/v1/passthroughs/"+id); status != 204 {
		t.Fatalf("withdraw: %d", status)
	}
	if got := q.Pending(); len(got) != 0 || !pub.ended(id) {
		t.Errorf("after the withdrawal: pending %+v, published %+v", got, pub.all())
	}
}

// deleteAgent sends DELETE path to the agent API where f reaches it, and
// returns the status.
func deleteAgent(t *testing.T, f transport.AgentForward, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, "http://nearhost"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := agentClient(f).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The queue tells onAdd of each request once it is published, and not of
// the notices, which wait for no answer.
func TestApprovalsTellOnAdd(t *testing.T) {
	clock := newFakeClock()
	q, pub, _ := newQueue(t, clock)
	var added []string
	q.onAdd = func(a control.Approval) {
		if !pub.published(a.ID) {
			t.Errorf("onAdd of %s before it was published", a.ID)
		}
		added = append(added, a.ID)
	}
	q.ReviewPassthrough(agentapi.PassRequest{ID: "p1", Expires: clock.now().Add(time.Minute)})
	q.Review(agentapi.Proposal{ID: "a1", Expires: clock.now().Add(time.Minute)})
	q.ReviewDocument(agentapi.DocRequest{ID: "d1", Expires: clock.now().Add(time.Minute)})
	q.Opened(agentapi.Passthrough{ID: "o1"})
	q.Shown("mini", "claude-code", "plan.md")
	if want := []string{"p1", "a1", "d1"}; !slices.Equal(added, want) {
		t.Errorf("onAdd told of %v, want %v", added, want)
	}
}

// published reports whether the approval id has been published.
func (p *published) published(id string) bool {
	return slices.ContainsFunc(p.all(), func(e control.Envelope) bool {
		return e.Kind == control.KindApproval && e.Approval != nil && e.Approval.ID == id
	})
}

func TestApprovalLine(t *testing.T) {
	pass := control.Approval{ID: "p1", Pass: &agentapi.PassRequest{Origin: "mini", Requester: "claude-code", Remote: 5173}}
	doc := control.Approval{ID: "d1", Doc: &agentapi.DocRequest{Origin: "mini", Requester: "codex", Doc: "docs/plan.md"}}
	prop := control.Approval{ID: "a1", Proposal: &agentapi.Proposal{Origin: "mini", Path: "/srv/app"}}
	odd := control.Approval{ID: "p2", Pass: &agentapi.PassRequest{Origin: "mi\x1b[2Jni", Requester: "my-tool", Remote: 80}}
	local := control.Approval{ID: "d2", Doc: &agentapi.DocRequest{Origin: config.LocalServer, Requester: "agent", Doc: "plan.md"}}
	page := "https://nearhost.localhost:6327"
	for _, tc := range []struct {
		a          control.Approval
		page, want string
	}{
		{pass, page, "Claude Code on mini asks to open port 5173. Answer at https://nearhost.localhost:6327/approve/p1"},
		{doc, page, "Codex on mini asks to show docs/plan.md. Answer at https://nearhost.localhost:6327/approve/d1"},
		{prop, page, "A coding agent on mini proposes adding /srv/app. Review it with nearhost, or decline it at https://nearhost.localhost:6327/approve/a1"},
		{pass, "", "Claude Code on mini asks to open port 5173. Open nearhost to answer."},
		{doc, "", "Codex on mini asks to show docs/plan.md. Open nearhost to answer."},
		{prop, "", "A coding agent on mini proposes adding /srv/app. Open nearhost to answer."},
		{local, "", "An agent on this machine asks to show plan.md. Open nearhost to answer."},
	} {
		if got := approvalLine(tc.a, tc.page); got != tc.want {
			t.Errorf("approvalLine(%s, %q)\n got %q\nwant %q", tc.a.ID, tc.page, got, tc.want)
		}
	}
	// What an agent sends cannot act on the terminal.
	if got := approvalLine(odd, ""); strings.ContainsRune(got, 0x1b) {
		t.Errorf("approvalLine kept an escape: %q", got)
	}
}

// A line that finds the writer's buffer full is dropped, and the output is
// told once that some were missed.
func TestLineWriterDropsWhenFull(t *testing.T) {
	var buf syncBuffer
	out := &gatedWriter{out: &buf}
	out.hold()
	t.Cleanup(out.release)
	w := newLineWriter(out, 1)
	w.put("a")
	testutil.WaitUntil(t, 5*time.Second, func() bool { return len(w.lines) == 0 }, "the writer to take the first line")
	w.put("b") // waits in the buffer
	w.put("c") // finds it full
	out.release()
	w.close()
	w.put("d") // after close: dropped, and no panic
	if got, want := buf.String(), "a\nb\n"+missedLines+"\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// A line dropped while the writer drains its last one still gets the
// note, at once rather than with the next line: here the writer takes the
// line that filled its buffer before the dropped one is counted.
func TestLineWriterSaysMissedWithoutANextLine(t *testing.T) {
	var buf syncBuffer
	w := makeLineWriter(&buf, 1)
	w.put("a") // fills the buffer
	w.put("b") // dropped and counted
	<-w.lines  // the writer took "a" before "b" was counted
	go w.run()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return buf.String() == missedLines+"\n" }, "the note that a line was missed")
	w.close()
}

// A Yes on the page to a passthrough whose server is not connected shows
// why it could not open, through the whole chain from the page to the
// agent API, rather than that the request was answered elsewhere.
func TestPageShowsWhyAPassthroughDidNotOpen(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	// The engine is never attached, so the passer finds no server
	// connected, as when it drops before the answer.
	f, _ := opts.Agent("mini")
	q, _, _ := newQueue(t, newFakeClock())
	q.now = time.Now // the agent API's requests expire by the real clock
	a.review(q)
	core := &coreBackend{coreOps: &coreOps{eng: engine.New(&config.Config{}, opts), agents: a, approvals: q}}
	status, out := postPass(t, f, "")
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)

	form := url.Values{"answer": {"yes"}}
	r := httptest.NewRequest(http.MethodPost, "/approve/"+id, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	page.New(page.Options{Core: core}).ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != http.StatusBadGateway || !strings.Contains(body, html.EscapeString(agentapi.ErrNotConnected.Error())) {
		t.Errorf("a Yes that could not open: %d\n%s", w.Code, body)
	}
	if strings.Contains(body, "This request has been answered or has ended.") || strings.Contains(body, "<form") {
		t.Errorf("a Yes that could not open reads as answered elsewhere, or offers answers:\n%s", body)
	}
	if got := q.Pending(); len(got) != 0 {
		t.Errorf("the request still waits: %+v", got)
	}
	if out := getAgentJSON(t, f, "/v1/passthroughs/"+id); out["state"] != "declined" {
		t.Errorf("the agent API says %v", out)
	}
}
