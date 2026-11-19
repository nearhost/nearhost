package agentapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeShower serves trees in memory. Resolve takes the sent root as the
// tree's root, or fails with resolveErr; Show records each call, waits for
// gate to close when set, and fails with showErr, or serves the tree for
// the requester that first showed it.
type fakeShower struct {
	mu         sync.Mutex
	gate       chan struct{}
	resolveErr error
	showErr    error
	served     map[[2]string]string // requester, by origin and root
	shows      []DocRequest         // Origin, Root, Doc and Requester of each Show
}

func (f *fakeShower) Resolve(_ context.Context, origin, p, root string) (Resolved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return Resolved{}, f.resolveErr
	}
	// A Windows laptop's paths are drive paths; the document is a slash
	// path under its root either way.
	if drivePath(p) {
		rel, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			return Resolved{Root: filepath.Dir(p), Doc: filepath.Base(p)}, nil
		}
		return Resolved{Root: root, Doc: filepath.ToSlash(rel)}, nil
	}
	rel := strings.TrimPrefix(p, strings.TrimSuffix(root, "/")+"/")
	if rel == p {
		return Resolved{Root: path.Dir(p), Doc: path.Base(p)}, nil
	}
	return Resolved{Root: root, Doc: rel}, nil
}

func (f *fakeShower) Served(origin, root string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req, ok := f.served[[2]string{origin, root}]
	return req, ok
}

func (f *fakeShower) Show(_ context.Context, origin, root, doc, requester string) (string, error) {
	f.mu.Lock()
	f.shows = append(f.shows, DocRequest{Origin: origin, Root: root, Doc: doc, Requester: requester})
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.showErr != nil {
		return "", f.showErr
	}
	key := [2]string{origin, root}
	if _, ok := f.served[key]; !ok {
		f.served[key] = requester
	}
	return "https://localhost:6327/d/tree/" + doc, nil
}

func (f *fakeShower) showed() []DocRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DocRequest(nil), f.shows...)
}

type docEnv struct {
	s      *Server
	r      *fakeReviewer
	sh     *fakeShower
	g      *fakeGrants // the documents grants
	pg     *fakeGrants // the passthrough grants
	c      *clock
	local  string
	remote string // apollo's socket
}

// newDocServer runs a server with documents and passthroughs on local's
// and apollo's sockets.
func newDocServer(t *testing.T, review bool) *docEnv {
	t.Helper()
	e := &docEnv{
		r:  &fakeReviewer{},
		sh: &fakeShower{served: map[[2]string]string{}},
		g:  &fakeGrants{tokens: map[[2]string]string{}},
		pg: &fakeGrants{tokens: map[[2]string]string{}},
		c:  &clock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)},
	}
	e.s = New(Options{
		ID: "laptop-id", Laptop: "dev@laptop", Source: fakeSource{"apollo": apollo},
		Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: e.pg,
		Shower: e.sh, DocGrants: e.g, Now: e.c.Now,
	})
	if review {
		e.s.SetReviewer(e.r)
	}
	dir := shortDir(t)
	e.local, e.remote = filepath.Join(dir, "local.sock"), filepath.Join(dir, "apollo.sock")
	for origin, sock := range map[string]string{"local": e.local, "apollo": e.remote} {
		if err := e.s.Listen(origin, sock); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(e.s.Close)
	return e
}

// ask posts a document request on apollo's socket.
func (e *docEnv) ask(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return call(t, e.remote, "POST", "/v1/documents", body)
}

// docBody is a request for doc under /srv/app, by requester, with extra
// JSON fields appended.
func docBody(doc, requester, extra string) string {
	return fmt.Sprintf(`{"path": "/srv/app/%s", "root": "/srv/app", "requester": %q%s}`, doc, requester, extra)
}

func TestDocumentsCapability(t *testing.T) {
	e := newDocServer(t, true)
	_, out := call(t, e.remote, "GET", "/v1", "")
	caps := fmt.Sprint(out["capabilities"])
	if caps != "[documents passthrough]" || out["id"] != "laptop-id" {
		t.Fatalf("with a Shower: %v", out)
	}

	// Documents alone give the id too.
	s := New(Options{ID: "laptop-id", Source: fakeSource{}, Shower: e.sh, DocGrants: e.g})
	sock := filepath.Join(shortDir(t), "a.sock")
	if err := s.Listen("local", sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	_, out = call(t, sock, "GET", "/v1", "")
	if fmt.Sprint(out["capabilities"]) != "[documents]" || out["id"] != "laptop-id" {
		t.Fatalf("documents alone: %v", out)
	}

	// A Shower without DocGrants is no documents.
	s = New(Options{ID: "laptop-id", Source: fakeSource{}, Shower: e.sh})
	sock = filepath.Join(shortDir(t), "b.sock")
	if err := s.Listen("local", sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	_, out = call(t, sock, "GET", "/v1", "")
	if fmt.Sprint(out["capabilities"]) != "[]" {
		t.Fatalf("without DocGrants: %v", out)
	}
	if _, ok := out["id"]; ok {
		t.Fatalf("without documents or passthrough, an id: %v", out)
	}
	if st, _ := call(t, sock, "POST", "/v1/documents", docBody("plan.md", "claude-code", "")); st != 404 {
		t.Fatalf("POST without documents: %d", st)
	}
}

func TestDocumentLocalAllowed(t *testing.T) {
	e := newDocServer(t, true)
	st, out := call(t, e.local, "POST", "/v1/documents",
		`{"path": "/home/dev/app/docs/plan.md", "root": "/home/dev/app", "requester": "claude-code", "session": "s1", "label": "the plan"}`)
	if st != 202 || out["id"] == nil || out["expires_at"] != "2026-10-28T12:10:00Z" {
		t.Fatalf("%d %v", st, out)
	}
	got := e.r.docPrompts()
	want := DocRequest{ID: out["id"].(string), Origin: "local", Requester: "claude-code", Session: "s1", Label: "the plan",
		Root: "/home/dev/app", Doc: "docs/plan.md", CanAlways: true, Expires: e.c.Now().Add(10 * time.Minute)}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(e.sh.showed()) != 0 {
		t.Fatal("shown before the answer")
	}
}

func TestDocumentValidation(t *testing.T) {
	e := newDocServer(t, true)
	for name, c := range map[string]struct{ body, field string }{
		"relative path": {`{"path": "plan.md", "root": "/srv/app", "requester": "claude-code"}`, "path"},
		"path with NUL": {`{"path": "/srv/app/pl\u0000an.md", "root": "/srv/app", "requester": "claude-code"}`, "path"},
		"relative root": {`{"path": "/srv/app/plan.md", "root": "app", "requester": "claude-code"}`, "root"},
		"long path":     {`{"path": "/` + strings.Repeat("a", 1100) + `.md", "root": "/srv/app", "requester": "claude-code"}`, "path"},
		"bad requester": {docBody("plan.md", "Claude", ""), "requester"},
		"long session":  {docBody("plan.md", "claude-code", `, "session": "`+strings.Repeat("s", 129)+`"`), "session"},
		"label newline": {docBody("plan.md", "claude-code", `, "label": "two\nlines"`), "label"},
	} {
		st, out := e.ask(t, c.body)
		msg, _ := out["error"].(string)
		if st != 400 || !strings.HasPrefix(msg, c.field+" ") {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}

	for name, c := range map[string]struct {
		err    error
		status int
		want   string
	}{
		"bad document": {fmt.Errorf("%w: plan.txt isn't markdown or HTML", ErrBadDocument), 400, "plan.txt isn't markdown or HTML"},
		"not there":    {fmt.Errorf("stat /srv/app/plan.md: %w", fs.ErrNotExist), 404, "/srv/app/plan.md"},
		"disconnected": {fmt.Errorf("reading: %w", ErrNotConnected), 409, "select apollo in Nearhost and press c"},
		"other":        {errors.New("sftp broke"), 502, "sftp broke"},
	} {
		e.sh.mu.Lock()
		e.sh.resolveErr = c.err
		e.sh.mu.Unlock()
		st, out := e.ask(t, docBody("plan.md", "claude-code", ""))
		msg, _ := out["error"].(string)
		if st != c.status || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}
	if len(e.r.docPrompts()) != 0 || len(e.sh.showed()) != 0 {
		t.Fatalf("a bad request went through: %v %v", e.r.docPrompts(), e.sh.showed())
	}
}

func TestDocumentAnswerYes(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("docs/plan.md", "claude-code", `, "session": "s1"`))
	id := out["id"].(string)
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); out["state"] != "pending" {
		t.Fatalf("before the answer: %v", out)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		if err := e.s.AnswerDocument(context.Background(), id, AnswerYes); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	_, out = call(t, e.remote, "GET", "/v1/documents/"+id+"?wait=5", "")
	if time.Since(start) > 3*time.Second {
		t.Fatal("the long poll did not return early")
	}
	if out["state"] != "open" || out["url"] != "https://localhost:6327/d/tree/docs/plan.md" {
		t.Fatalf("after yes: %v", out)
	}
	if _, ok := out["token"]; ok {
		t.Fatalf("a token after yes: %v", out)
	}
	shows := e.sh.showed()
	want := DocRequest{Origin: "apollo", Root: "/srv/app", Doc: "docs/plan.md", Requester: "claude-code"}
	if len(shows) != 1 || shows[0] != want {
		t.Fatalf("shows %+v", shows)
	}
	if err := e.s.AnswerDocument(context.Background(), id, AnswerYes); !errors.Is(err, ErrPassEnded) {
		t.Fatalf("a second answer: %v", err)
	}
	if err := e.s.AnswerDocument(context.Background(), "nope", AnswerYes); !errors.Is(err, ErrPassEnded) {
		t.Fatalf("an unknown id: %v", err)
	}
	if err := e.s.AnswerDocument(context.Background(), "nope", AnswerNo); err != nil {
		t.Fatalf("no to an unknown id: %v", err)
	}
	if len(e.r.shownDocs()) != 0 {
		t.Fatal("a prompted document was announced as shown without asking")
	}
	_, list := call(t, e.remote, "GET", "/v1/documents", "")
	docs, _ := list["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("list: %v", list)
	}
	d := docs[0].(map[string]any)
	if d["state"] != "open" || d["root"] != "/srv/app" || d["path"] != "docs/plan.md" || d["requester"] != "claude-code" || d["session"] != "s1" {
		t.Fatalf("listed %v", d)
	}
}

func TestDocumentAnswerNoAndFailures(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("a.md", "claude-code", ""))
	id := out["id"].(string)
	if err := e.s.AnswerDocument(context.Background(), id, AnswerNo); err != nil {
		t.Fatal(err)
	}
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); out["state"] != "declined" {
		t.Fatalf("after no: %v", out)
	}

	// A No holds back the next request for backoff.
	e.c.add(backoff)
	_, out = e.ask(t, docBody("b.md", "agent", ""))
	id = out["id"].(string)
	if e.r.docPrompts()[1].CanAlways {
		t.Fatal("agent was offered always allow")
	}
	if err := e.s.AnswerDocument(context.Background(), id, AnswerAlways); err == nil || errors.Is(err, ErrPassEnded) {
		t.Fatalf("always allow for agent: %v", err)
	}

	e.sh.mu.Lock()
	e.sh.showErr = fmt.Errorf("%w: --web-port is 0", ErrNoPage)
	e.sh.mu.Unlock()
	err := e.s.AnswerDocument(context.Background(), id, AnswerYes)
	if !errors.Is(err, ErrPassEnded) || !errors.Is(err, ErrNoPage) {
		t.Fatalf("a failed show: %v", err)
	}
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); out["state"] != "declined" || !strings.Contains(fmt.Sprint(out["reason"]), "--web-port is 0") {
		t.Fatalf("after a failed show: %v", out)
	}

	e.sh.mu.Lock()
	e.sh.showErr = nil
	e.sh.mu.Unlock()
	e.g.mu.Lock()
	e.g.allowErr = errors.New("disk full")
	e.g.mu.Unlock()
	_, out = e.ask(t, docBody("c.md", "claude-code", ""))
	id = out["id"].(string)
	err = e.s.AnswerDocument(context.Background(), id, AnswerAlways)
	if !errors.Is(err, ErrAllowNotSaved) || errors.Is(err, ErrPassEnded) {
		t.Fatalf("a grant that failed to save: %v", err)
	}
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); out["state"] != "open" || out["token"] != nil {
		t.Fatalf("after the failed grant: %v", out)
	}
}

func TestDocumentAlways(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("plan.md", "claude-code", ""))
	id := out["id"].(string)
	if err := e.s.AnswerDocument(context.Background(), id, AnswerAlways); err != nil {
		t.Fatal(err)
	}
	_, out = call(t, e.remote, "GET", "/v1/documents/"+id+"?wait=5", "")
	if out["state"] != "open" || out["token"] != "tok-1" || out["url"] == nil {
		t.Fatalf("first poll: %v", out)
	}
	_, out = call(t, e.remote, "GET", "/v1/documents/"+id, "")
	if _, ok := out["token"]; ok || out["state"] != "open" {
		t.Fatalf("second poll: %v", out)
	}
	if got := e.g.allowed(); len(got) != 1 || got[0] != [2]string{"apollo", "claude-code"} {
		t.Fatalf("allowed %v", got)
	}
	if len(e.pg.allowed()) != 0 {
		t.Fatal("the passthrough grants were given one")
	}

	// A token opens a new tree without a prompt.
	st, out := call(t, e.remote, "POST", "/v1/documents",
		`{"path": "/srv/other/spec.md", "root": "/srv/other", "requester": "claude-code", "token": "tok-1"}`)
	if st != 201 || out["state"] != "open" || out["url"] != "https://localhost:6327/d/tree/spec.md" {
		t.Fatalf("with the token: %d %v", st, out)
	}
	if _, ok := out["token"]; ok {
		t.Fatalf("a token in the reply: %v", out)
	}
	if n := len(e.r.docPrompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
	if got := e.r.shownDocs(); len(got) != 1 || got[0] != [3]string{"apollo", "claude-code", "spec.md"} {
		t.Fatalf("shown %v", got)
	}

	st, out = call(t, e.remote, "POST", "/v1/documents",
		`{"path": "/srv/third/spec.md", "root": "/srv/third", "requester": "claude-code", "token": "wrong"}`)
	if st != 401 || !strings.Contains(fmt.Sprint(out["error"]), "revoked or never existed") {
		t.Fatalf("a wrong token: %d %v", st, out)
	}
}

func TestDocumentRepeats(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		e := newDocServer(t, true)
		body := docBody("plan.md", "claude-code", `, "session": "s1"`)
		_, first := e.ask(t, body)
		st, again := e.ask(t, body)
		if st != 202 || again["id"] != first["id"] {
			t.Fatalf("again: %d %v, first %v", st, again, first)
		}
		if n := len(e.r.docPrompts()); n != 1 {
			t.Fatalf("%d prompts", n)
		}
		// Another session is another request.
		if st, other := e.ask(t, docBody("plan.md", "claude-code", `, "session": "s2"`)); st != 202 || other["id"] == first["id"] {
			t.Fatalf("another session: %d %v", st, other)
		}
	})
	t.Run("served tree", func(t *testing.T) {
		e := newDocServer(t, true)
		_, out := e.ask(t, docBody("plan.md", "claude-code", ""))
		if err := e.s.AnswerDocument(context.Background(), out["id"].(string), AnswerYes); err != nil {
			t.Fatal(err)
		}
		st, out := e.ask(t, docBody("docs/spec.md", "claude-code", ""))
		if st != 201 || out["state"] != "open" || out["url"] != "https://localhost:6327/d/tree/docs/spec.md" {
			t.Fatalf("another document in the tree: %d %v", st, out)
		}
		if n := len(e.r.docPrompts()); n != 1 {
			t.Fatalf("%d prompts", n)
		}
		if n := len(e.sh.showed()); n != 2 {
			t.Fatalf("%d shows", n)
		}
		if got := e.r.shownDocs(); len(got) != 1 || got[0] != [3]string{"apollo", "claude-code", "docs/spec.md"} {
			t.Fatalf("shown %v", got)
		}
	})
	t.Run("agent naming the tree", func(t *testing.T) {
		e := newDocServer(t, true)
		_, out := e.ask(t, docBody("plan.md", "claude-code", ""))
		if err := e.s.AnswerDocument(context.Background(), out["id"].(string), AnswerYes); err != nil {
			t.Fatal(err)
		}
		if st, out := e.ask(t, docBody("docs/spec.md", "agent", "")); st != 202 || out["id"] == nil {
			t.Fatalf("agent: %d %v", st, out)
		}
		if st, _ := e.ask(t, docBody("docs/spec.md", "codex", "")); st != 202 {
			t.Fatalf("another requester: %d", st)
		}
		if n := len(e.r.docPrompts()); n != 3 {
			t.Fatalf("%d prompts", n)
		}
	})
	t.Run("agent's own tree", func(t *testing.T) {
		e := newDocServer(t, true)
		_, out := e.ask(t, docBody("plan.md", "agent", ""))
		if err := e.s.AnswerDocument(context.Background(), out["id"].(string), AnswerYes); err != nil {
			t.Fatal(err)
		}
		if st, _ := e.ask(t, docBody("docs/spec.md", "agent", "")); st != 202 {
			t.Fatalf("agent in a tree approved for agent: %d", st)
		}
	})
}

// A document opened again by the same requester on the same server within
// reopenWindow gets the first answer again and opens no second tab.
func TestDocumentOpenedTwiceOpensOnce(t *testing.T) {
	e := newDocServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	body := docBody("plan.md", "claude-code", `, "token": "good"`)
	st, first := e.ask(t, body)
	if st != 201 || first["state"] != "open" {
		t.Fatalf("first: %d %v", st, first)
	}
	e.c.add(time.Second)
	st, again := e.ask(t, body)
	if st != 201 || fmt.Sprint(again) != fmt.Sprint(first) {
		t.Fatalf("again: %d %v, first %v", st, again, first)
	}
	if n := len(e.sh.showed()); n != 1 {
		t.Fatalf("%d shows, want 1", n)
	}
	if n := len(e.r.shownDocs()); n != 1 {
		t.Fatalf("%d shown notices, want 1", n)
	}

	// Another document, or another requester, opens.
	if st, out := e.ask(t, docBody("spec.md", "claude-code", `, "token": "good"`)); st != 201 || out["id"] == first["id"] {
		t.Fatalf("another document: %d %v", st, out)
	}
	e.sh.mu.Lock()
	e.sh.served[[2]string{"apollo", "/srv/app"}] = "codex"
	e.sh.mu.Unlock()
	if st, out := e.ask(t, docBody("plan.md", "codex", "")); st != 201 || out["id"] == first["id"] {
		t.Fatalf("another requester: %d %v", st, out)
	}
	if n := len(e.sh.showed()); n != 3 {
		t.Fatalf("%d shows, want 3", n)
	}

	// Past the window it opens again.
	e.c.add(reopenWindow)
	if st, out := e.ask(t, body); st != 201 || out["id"] == first["id"] {
		t.Fatalf("after the window: %d %v", st, out)
	}
	if n := len(e.sh.showed()); n != 4 {
		t.Fatalf("%d shows, want 4", n)
	}
}

// openingTwice starts an open of plan.md on e with a token, holds it in
// Show, and starts a second that waits for it. It returns both results and
// the gate that releases Show.
func openingTwice(t *testing.T, e *docEnv) (first, second <-chan callResult, gate chan struct{}) {
	t.Helper()
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	gate = make(chan struct{})
	e.sh.mu.Lock()
	e.sh.gate = gate
	e.sh.mu.Unlock()
	waiting := make(chan struct{}, 1)
	e.s.mu.Lock()
	e.s.reopenWaiting = func() { waiting <- struct{}{} }
	e.s.mu.Unlock()
	body := docBody("plan.md", "claude-code", `, "token": "good"`)
	first = callAsync(e.remote, "POST", "/v1/documents", body)
	for end := time.Now().Add(5 * time.Second); len(e.sh.showed()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the first open never reached Show")
		}
	}
	second = callAsync(e.remote, "POST", "/v1/documents", body)
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("the second open never waited for the first")
	}
	return first, second, gate
}

// A second open of a document still being shown waits for the first and
// gets its answer.
func TestDocumentOpenedTwiceAtOnceOpensOnce(t *testing.T) {
	e := newDocServer(t, true)
	first, second, gate := openingTwice(t, e)
	close(gate)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.st != 201 || b.st != 201 || a.out["id"] == nil || fmt.Sprint(a.out) != fmt.Sprint(b.out) {
		t.Fatalf("answers %+v and %+v", a, b)
	}
	if n := len(e.sh.showed()); n != 1 {
		t.Fatalf("%d shows, want 1", n)
	}
}

// When the Show a second open waits for fails, the second shows the
// document itself.
func TestDocumentOpenWaitingOnAFailedShowShowsAgain(t *testing.T) {
	e := newDocServer(t, true)
	first, second, gate := openingTwice(t, e)
	e.sh.mu.Lock()
	e.sh.showErr = errors.New("the tunnel dropped")
	e.sh.mu.Unlock()
	close(gate)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.st != http.StatusBadGateway || b.st != http.StatusBadGateway {
		t.Fatalf("answers %+v and %+v", a, b)
	}
	if n := len(e.sh.showed()); n != 2 {
		t.Fatalf("%d shows, want 2", n)
	}
}

// An open the user approved is never the answer to another request, as its
// reply may hold the Always allow token.
func TestDocumentPromptedOpenIsNotRepeated(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("plan.md", "claude-code", ""))
	id := out["id"].(string)
	if err := e.s.AnswerDocument(context.Background(), id, AnswerAlways); err != nil {
		t.Fatal(err)
	}
	// The tree is served for claude-code now, so this opens without asking.
	st, again := e.ask(t, docBody("plan.md", "claude-code", ""))
	if st != 201 || again["id"] == id {
		t.Fatalf("again: %d %v", st, again)
	}
	if _, ok := again["token"]; ok {
		t.Fatalf("a token in the reply: %v", again)
	}
	if n := len(e.sh.showed()); n != 2 {
		t.Fatalf("%d shows, want 2", n)
	}
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); out["token"] != "tok-1" {
		t.Fatalf("the approved request lost its token: %v", out)
	}
}

// A loop with a token, or in a served tree, can open maxFreeOpens documents
// a minute without a prompt; past that the user is asked.
func TestDocumentOpensWithoutAPromptAreCapped(t *testing.T) {
	e := newDocServer(t, true)
	// An open the user approved doesn't count.
	_, out := e.ask(t, docBody("approved.md", "claude-code", ""))
	if err := e.s.AnswerDocument(context.Background(), out["id"].(string), AnswerYes); err != nil {
		t.Fatal(err)
	}
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	e.g.tokens[[2]string{"local", "claude-code"}] = "good"
	tok := `, "token": "good"`
	for i := range maxFreeOpens {
		if st, out := e.ask(t, docBody(fmt.Sprintf("p%d.md", i), "claude-code", tok)); st != 201 {
			t.Fatalf("open %d: %d %v", i, st, out)
		}
		// A repeat answered with the first reply doesn't count.
		if st, out := e.ask(t, docBody(fmt.Sprintf("p%d.md", i), "claude-code", tok)); st != 201 {
			t.Fatalf("repeat %d: %d %v", i, st, out)
		}
	}
	st, out := e.ask(t, docBody("p5.md", "claude-code", tok))
	if st != 202 || out["id"] == nil {
		t.Fatalf("the sixth: %d %v", st, out)
	}
	if n := len(e.r.docPrompts()); n != 2 {
		t.Fatalf("%d prompts, want 2", n)
	}
	if n := len(e.sh.showed()); n != 1+maxFreeOpens {
		t.Fatalf("%d shows, want %d", n, 1+maxFreeOpens)
	}
	// Another origin has its own count.
	if st, out := call(t, e.local, "POST", "/v1/documents", docBody("p6.md", "claude-code", tok)); st != 201 {
		t.Fatalf("on local: %d %v", st, out)
	}
	// A minute on, opens go through without a prompt again.
	e.c.add(freeOpenWindow)
	if st, out := e.ask(t, docBody("p7.md", "claude-code", tok)); st != 201 {
		t.Fatalf("a minute on: %d %v", st, out)
	}
}

func TestDocumentLimit(t *testing.T) {
	e := newDocServer(t, true)
	for port := 3000; port < 3003; port++ {
		if st, out := call(t, e.remote, "POST", "/v1/passthroughs", fmt.Sprintf(`{"port": %d, "requester": "claude-code"}`, port)); st != 202 {
			t.Fatalf("passthrough %d: %d %v", port, st, out)
		}
	}
	for i := range 3 {
		if st, out := e.ask(t, docBody(fmt.Sprintf("p%d.md", i), "claude-code", "")); st != 202 {
			t.Fatalf("document %d: %d %v", i, st, out)
		}
	}
	if st, out := e.ask(t, docBody("p3.md", "claude-code", "")); st != 429 {
		t.Fatalf("a fourth: %d %v", st, out)
	}
	// Another origin has its own limit.
	if st, out := call(t, e.local, "POST", "/v1/documents", docBody("p3.md", "claude-code", "")); st != 202 {
		t.Fatalf("on local: %d %v", st, out)
	}
}

func TestDocumentHeadless(t *testing.T) {
	e := newDocServer(t, false)
	st, out := e.ask(t, docBody("plan.md", "claude-code", ""))
	if st != 409 || !strings.Contains(fmt.Sprint(out["error"]), "nearhost up") {
		t.Fatalf("%d %v", st, out)
	}
	// A valid token opens it all the same.
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "tok"
	if st, out := e.ask(t, docBody("plan.md", "claude-code", `, "token": "tok"`)); st != 201 || out["state"] != "open" {
		t.Fatalf("with a token: %d %v", st, out)
	}
}

func TestDocumentNoPage(t *testing.T) {
	e := newDocServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "tok"
	e.sh.showErr = fmt.Errorf("%w: --web-port is 0", ErrNoPage)
	st, out := e.ask(t, docBody("plan.md", "claude-code", `, "token": "tok"`))
	if st != 409 || !strings.Contains(fmt.Sprint(out["error"]), "--web-port is 0") {
		t.Fatalf("%d %v", st, out)
	}
	if len(e.r.shownDocs()) != 0 {
		t.Fatal("a document that failed to show was announced")
	}
}

func TestDocumentExpires(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("plan.md", "claude-code", ""))
	id := out["id"].(string)
	e.c.add(10 * time.Minute)
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id+"?wait=5", ""); out["state"] != "expired" {
		t.Fatalf("%v", out)
	}
	if err := e.s.AnswerDocument(context.Background(), id, AnswerYes); !errors.Is(err, ErrPassEnded) {
		t.Fatalf("an answer after expiry: %v", err)
	}
	// It no longer counts, so another may be asked.
	if st, _ := e.ask(t, docBody("plan.md", "claude-code", "")); st != 202 {
		t.Fatalf("asking again: %d", st)
	}
	// Another origin can't read it.
	if st := status(t, e.local, http.MethodGet, "/v1/documents/"+id); st != 404 {
		t.Fatalf("from local: %d", st)
	}
}

func TestDocumentListShowsTheLastOpened(t *testing.T) {
	e := newDocServer(t, true)
	_, a := e.ask(t, docBody("a.md", "claude-code", ""))
	_, b := e.ask(t, docBody("b.md", "claude-code", ""))
	// b is answered first, so a, asked earlier, is the one opened last.
	for _, id := range []string{b["id"].(string), a["id"].(string)} {
		if err := e.s.AnswerDocument(context.Background(), id, AnswerYes); err != nil {
			t.Fatal(err)
		}
	}
	_, list := call(t, e.remote, "GET", "/v1/documents", "")
	docs, _ := list["documents"].([]any)
	if len(docs) != 1 || docs[0].(map[string]any)["path"] != "a.md" {
		t.Fatalf("list %v, want a.md, the document opened last", list)
	}
}

func TestDocumentRefusesANetworkPath(t *testing.T) {
	unc := `\\server\share\plan.md`
	if msg := badDocPath("path", unc); !strings.Contains(msg, "network paths") {
		t.Errorf("badDocPath(%q) = %q; want a network path message", unc, msg)
	}
	e := newDocServer(t, true)
	body := fmt.Sprintf(`{"path": %q, "root": "/srv/app", "requester": "claude-code"}`, unc)
	st, out := e.ask(t, body)
	if msg, _ := out["error"].(string); st != 400 || !strings.Contains(msg, `\\server\share`) {
		t.Errorf("%d %v", st, out)
	}
	if msg := badDocPath("path", "/srv/app/plan.md"); msg != "" {
		t.Errorf("badDocPath(/srv/app/plan.md) = %q", msg)
	}
}

func TestPlainDrivePath(t *testing.T) {
	for in, want := range map[string]string{
		`\\?\C:\x`:       `C:\x`,
		`//?/D:/y`:       `//?/D:/y`,
		`\\?\UNC\s\sh\x`: `\\?\UNC\s\sh\x`,
		`\\?\C:`:         `\\?\C:`,
		`/srv/x`:         `/srv/x`,
		`C:\x`:           `C:\x`,
	} {
		if got := plainDrivePath(in); got != want {
			t.Errorf("plainDrivePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBadDocPathWindowsForms(t *testing.T) {
	for _, v := range []string{`\\?\UNC\s\sh\p.md`, `\\?\unc\s\sh\p.md`, `\\s\sh\p.md`} {
		if msg := badDocPath("path", v); !strings.Contains(msg, "network path") {
			t.Errorf("badDocPath(%q) = %q; want a network path message", v, msg)
		}
	}
	for _, v := range []string{`\\.\pipe\x`, `\\?\Volume{1}\x`, `\\?\1:\x`} {
		if msg := badDocPath("path", v); !strings.Contains(msg, "Windows device path") {
			t.Errorf("badDocPath(%q) = %q; want a device path message", v, msg)
		}
	}
}

func TestDocumentAcceptsAnExtendedLengthDrivePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("C:\\ paths are absolute only on Windows")
	}
	if msg := badDocPath("path", plainDrivePath(`\\?\C:\Users\me\notes\plan.md`)); msg != "" {
		t.Errorf("badDocPath = %q", msg)
	}
}

func TestUnixSlashQuestionPathsPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("//?/ is a Windows prefix there")
	}
	for _, v := range []string{`//?/C:/x`, `//?/unc/s/x`} {
		if plainDrivePath(v) != v {
			t.Errorf("plainDrivePath(%q) changed it", v)
		}
		if msg := badDocPath("path", v); msg != "" {
			t.Errorf("badDocPath(%q) = %q", v, msg)
		}
	}
}

func TestDocumentRefusesADrivePathFromAServer(t *testing.T) {
	e := newDocServer(t, true)
	for _, v := range []string{`C:\srv\plan.md`, `\\?\C:\srv\plan.md`} {
		body := fmt.Sprintf(`{"path": %q, "root": "/srv/app", "requester": "claude-code"}`, v)
		if st, out := e.ask(t, body); st != 400 {
			t.Errorf("%s: %d %v, want 400", v, st, out)
		}
	}
	if msg := remoteDrivePath("apollo", "path", `C:\x`); !strings.Contains(msg, "slash path") {
		t.Errorf("remoteDrivePath(apollo, C:\\x) = %q", msg)
	}
	for _, c := range [][2]string{{"local", `C:\x`}, {"apollo", "/srv/x"}} {
		if msg := remoteDrivePath(c[0], "path", c[1]); msg != "" {
			t.Errorf("remoteDrivePath(%s, %s) = %q", c[0], c[1], msg)
		}
	}
}

func TestDocumentRefusedWaits(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("a.md", "claude-code", ""))
	if err := e.s.AnswerDocument(context.Background(), out["id"].(string), AnswerNo); err != nil {
		t.Fatal(err)
	}
	e.c.add(10 * time.Second)
	st, retry, out := askFull(t, e.remote, "/v1/documents", docBody("b.md", "claude-code", ""))
	if st != http.StatusTooManyRequests || retry != "20" || out["error"] != "refused; ask again in 20 s" {
		t.Fatalf("at 10 s: %d %q %v", st, retry, out)
	}
	if n := len(e.r.docPrompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}

	// The laptop is not held back by apollo's No, nor are apollo's
	// passthroughs, nor a request with a valid token.
	if st, out := call(t, e.local, "POST", "/v1/documents", docBody("b.md", "claude-code", "")); st != http.StatusAccepted {
		t.Fatalf("local: %d %v", st, out)
	}
	if st, out := call(t, e.remote, "POST", "/v1/passthroughs", `{"port": 3001, "requester": "claude-code"}`); st != http.StatusAccepted {
		t.Fatalf("a passthrough: %d %v", st, out)
	}
	tok, _ := e.g.Allow("apollo", "claude-code")
	if st, out := e.ask(t, `{"path": "/srv/other/c.md", "root": "/srv/other", "requester": "claude-code", "token": "`+tok+`"}`); st != http.StatusCreated {
		t.Fatalf("with a token: %d %v", st, out)
	}

	if st, _, out := askFull(t, e.remote, "/v1/documents", docBody("d.md", "codex", `, "session": "s9"`)); st != http.StatusTooManyRequests {
		t.Fatalf("another requester and session: %d %v", st, out)
	}

	e.c.add(21 * time.Second)
	if st, out := e.ask(t, docBody("b.md", "claude-code", "")); st != http.StatusAccepted {
		t.Fatalf("at 31 s: %d %v", st, out)
	}
	if n := len(e.r.docPrompts()); n != 3 {
		t.Fatalf("%d prompts", n)
	}
}

func TestDocumentOnlyANoHoldsBack(t *testing.T) {
	e := newDocServer(t, true)
	_, out := e.ask(t, docBody("a.md", "claude-code", ""))
	id := out["id"].(string)
	e.c.add(10 * time.Minute)
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); out["state"] != "expired" {
		t.Fatalf("not expired: %v", out)
	}
	if err := e.s.AnswerDocument(context.Background(), id, AnswerNo); err == nil {
		t.Fatal("no on an expired request was taken")
	}
	_, out = e.ask(t, docBody("b.md", "claude-code", ""))
	if out["id"] == nil {
		t.Fatalf("after an expiry: %v", out)
	}
	// A box closed after a failed answer is no refusal either.
	if err := e.s.AnswerDocument(context.Background(), out["id"].(string), AnswerClose); err != nil {
		t.Fatal(err)
	}
	if _, out := call(t, e.remote, "GET", "/v1/documents/"+out["id"].(string), ""); out["state"] != "declined" || out["reason"] == nil || out["held"] != nil {
		t.Fatalf("closed: %v", out)
	}
	if st, out := e.ask(t, docBody("c.md", "claude-code", "")); st != http.StatusAccepted {
		t.Fatalf("after a close: %d %v", st, out)
	}
}

func TestDocumentNoHoldsBackTheQueue(t *testing.T) {
	e := newDocServer(t, true)
	var ids []string
	for _, doc := range []string{"a.md", "b.md", "c.md"} {
		_, out := e.ask(t, docBody(doc, "claude-code", ""))
		ids = append(ids, out["id"].(string))
	}
	_, out := call(t, e.local, "POST", "/v1/documents", docBody("a.md", "claude-code", ""))
	local := out["id"].(string)
	_, out = call(t, e.remote, "POST", "/v1/passthroughs", `{"port": 3001, "requester": "claude-code"}`)
	pass := out["id"].(string)
	if err := e.s.AnswerDocument(context.Background(), ids[0], AnswerNo); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[1:] {
		_, out := call(t, e.remote, "GET", "/v1/documents/"+id+"?wait=5", "")
		if out["state"] != "declined" || out["held"] != true || out["retry_after"] != 30.0 || out["reason"] == nil {
			t.Fatalf("a queued one: %v", out)
		}
	}
	if _, out := call(t, e.local, "GET", "/v1/documents/"+local, ""); out["state"] != "pending" {
		t.Fatalf("the laptop's: %v", out)
	}
	if _, out := call(t, e.remote, "GET", "/v1/passthroughs/"+pass, ""); out["state"] != "pending" {
		t.Fatalf("apollo's passthrough: %v", out)
	}
	if len(e.sh.showed()) != 0 {
		t.Fatal("a held request was shown")
	}
}
