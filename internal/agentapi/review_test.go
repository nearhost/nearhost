package agentapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// wantEndedOnce fails unless the reviewer was shown id and then told once
// that it ended.
func wantEndedOnce(t *testing.T, r *fakeReviewer, id string) {
	t.Helper()
	if n := r.endedCount(id); n != 1 {
		t.Fatalf("Ended(%s) told %d times; log %v", id, n, r.events())
	}
	r.mu.Lock()
	first := append([]string(nil), r.endedFirst...)
	r.mu.Unlock()
	if len(first) != 0 {
		t.Fatalf("Ended came before the review for %v; log %v", first, r.events())
	}
}

// askPass asks apollo for a passthrough of port in session and returns the
// pending request's ID.
func askPass(t *testing.T, e *passEnv, port int, session string) string {
	t.Helper()
	st, out := e.ask(t, fmt.Sprintf(`{"port": %d, "requester": "claude-code", "session": %q}`, port, session))
	if st != 202 {
		t.Fatalf("asking for %d: %d %v", port, st, out)
	}
	return out["id"].(string)
}

func TestEndedAfterAnswer(t *testing.T) {
	e := newPassServer(t, true)
	id := askPass(t, e, 3000, "s")
	if err := e.s.AnswerPassthrough(context.Background(), id, AnswerNo); err != nil {
		t.Fatal(err)
	}
	wantEndedOnce(t, e.r, id)
	if got := e.r.events(); len(got) != 2 || got[0] != "review "+id || got[1] != "ended "+id {
		t.Fatalf("log %v", got)
	}

	// Yes ends it too, as it opens.
	e.c.add(backoff)
	yes := askPass(t, e, 3001, "s")
	if err := e.s.AnswerPassthrough(context.Background(), yes, AnswerYes); err != nil {
		t.Fatal(err)
	}
	wantEndedOnce(t, e.r, yes)

	// A Yes whose open fails ends it too, declined with why.
	e.c.add(backoff)
	e.p.mu.Lock()
	e.p.err = errors.New("apollo: not connected")
	e.p.mu.Unlock()
	failed := askPass(t, e, 3002, "s")
	if err := e.s.AnswerPassthrough(context.Background(), failed, AnswerYes); !errors.Is(err, ErrNotOpened) {
		t.Fatalf("a failed open: %v", err)
	}
	wantEndedOnce(t, e.r, failed)

	d := newDocServer(t, true)
	d.sh.mu.Lock()
	d.sh.showErr = errors.New("the tunnel dropped")
	d.sh.mu.Unlock()
	doc := askDoc(t, d, "plan.md")
	if err := d.s.AnswerDocument(context.Background(), doc, AnswerYes); !errors.Is(err, ErrNotOpened) {
		t.Fatalf("a failed show: %v", err)
	}
	wantEndedOnce(t, d.r, doc)
}

func TestEndedWhenWithdrawn(t *testing.T) {
	e := newPassServer(t, true)
	one := askPass(t, e, 3000, "s1")
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs/"+one); st != 204 {
		t.Fatalf("DELETE one: %d", st)
	}
	wantEndedOnce(t, e.r, one)

	a, b := askPass(t, e, 3001, "s2"), askPass(t, e, 3002, "s2")
	other := askPass(t, e, 3003, "s3")
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs?session=s2"); st != 204 {
		t.Fatalf("DELETE session: %d", st)
	}
	wantEndedOnce(t, e.r, a)
	wantEndedOnce(t, e.r, b)
	if n := e.r.endedCount(other); n != 0 {
		t.Fatalf("another session's request ended: %v", e.r.events())
	}
}

func TestEndedWhenHeld(t *testing.T) {
	e := newPassServer(t, true)
	a, b, c := askPass(t, e, 3000, "s"), askPass(t, e, 3001, "s"), askPass(t, e, 3002, "t")
	if err := e.s.AnswerPassthrough(context.Background(), a, AnswerNo); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b, c} {
		wantEndedOnce(t, e.r, id)
	}

	d := newDocServer(t, true)
	var ids []string
	for _, doc := range []string{"a.md", "b.md"} {
		st, out := d.ask(t, docBody(doc, "claude-code", ""))
		if st != 202 {
			t.Fatalf("%s: %d %v", doc, st, out)
		}
		ids = append(ids, out["id"].(string))
	}
	if err := d.s.AnswerDocument(context.Background(), ids[0], AnswerNo); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		wantEndedOnce(t, d.r, id)
	}
}

func TestEndedWhenProposalAnswered(t *testing.T) {
	s, r, _, _, remote := newServer(t, true)
	propose := func(path string) string {
		t.Helper()
		st, out := call(t, remote, "POST", "/v1/proposals", fmt.Sprintf(`{"path": %q}`, path))
		if st != 202 {
			t.Fatalf("%d %v", st, out)
		}
		return out["id"].(string)
	}
	yes, no := propose("/srv/one"), propose("/srv/two")
	s.Accept(yes, "one")
	s.Decline(no, "not now")
	wantEndedOnce(t, r, yes)
	wantEndedOnce(t, r, no)
	// A second answer tells nothing more.
	s.Accept(yes, "one")
	wantEndedOnce(t, r, yes)
}

func TestReloadsAreNeverEnded(t *testing.T) {
	s, r, rl, _, sock := reloadServer(t, true)
	rl.answer(waiting, nil)
	st, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	if st != 202 {
		t.Fatalf("%d %v", st, out)
	}
	s.ChangeAnswered("myapp", waiting.SHA256, true)
	if got := r.events(); len(got) != 0 {
		t.Fatalf("a reload was reviewed or ended: %v", got)
	}
}

// askDoc asks apollo to show doc and returns the pending request's ID.
func askDoc(t *testing.T, e *docEnv, doc string) string {
	t.Helper()
	st, out := e.ask(t, docBody(doc, "claude-code", ""))
	if st != 202 {
		t.Fatalf("asking for %s: %d %v", doc, st, out)
	}
	return out["id"].(string)
}

func TestEndedOnExpiry(t *testing.T) {
	e := newDocServer(t, true)
	id := askDoc(t, e, "plan.md")
	e.c.add(expiry)
	if st, out := call(t, e.remote, "GET", "/v1/documents/"+id, ""); st != 200 || out["state"] != string(StateExpired) {
		t.Fatalf("%d %v", st, out)
	}
	wantEndedOnce(t, e.r, id)
	call(t, e.remote, "GET", "/v1/documents", "")
	wantEndedOnce(t, e.r, id)
}

func TestEndedOnClose(t *testing.T) {
	e := newDocServer(t, true)
	id := askDoc(t, e, "plan.md")
	e.s.Close()
	wantEndedOnce(t, e.r, id)
	// An answer after the close tells nothing more.
	e.s.AnswerDocument(context.Background(), id, AnswerNo)
	wantEndedOnce(t, e.r, id)
}

func TestEndedOnlyForTheReviewerShown(t *testing.T) {
	e := newDocServer(t, true)
	id := askDoc(t, e, "plan.md")
	next := &fakeReviewer{}
	e.s.SetReviewer(next)
	if err := e.s.AnswerDocument(context.Background(), id, AnswerNo); err != nil {
		t.Fatal(err)
	}
	if got := next.events(); len(got) != 0 {
		t.Fatalf("the new reviewer was told: %v", got)
	}
	if got := e.r.endedCount(id); got != 0 {
		t.Fatalf("the old reviewer was told: %v", e.r.events())
	}
}

func TestEndedNotForOpensWithoutAsking(t *testing.T) {
	e := newPassServer(t, true)
	e.g.tokens[[2]string{"apollo", "claude-code"}] = "good"
	st, out := e.ask(t, `{"port": 3000, "requester": "claude-code", "token": "good"}`)
	if st != 201 {
		t.Fatalf("%d %v", st, out)
	}
	if st := status(t, e.remote, "DELETE", "/v1/passthroughs/"+out["id"].(string)); st != 204 {
		t.Fatalf("DELETE: %d", st)
	}
	if got := e.r.events(); len(got) != 0 {
		t.Fatalf("log %v", got)
	}
}

// TestReviewBeforeEnded withdraws each request as it is made, so the
// withdrawal can land between the request being recorded and being shown.
// The reviewer must never hear it ended before it was shown.
func TestReviewBeforeEnded(t *testing.T) {
	e := newPassServer(t, true)
	e.r.slow = time.Millisecond
	for i := range 200 {
		session := fmt.Sprintf("s%d", i)
		var wg sync.WaitGroup
		wg.Add(2)
		// The goroutines can't fail the test; a request that fails shows as
		// a missing review or Ended below.
		send := func(method, path, body string) {
			defer wg.Done()
			req, _ := http.NewRequest(method, "http://nearhost"+path, strings.NewReader(body))
			if resp, err := client(e.remote).Do(req); err == nil {
				resp.Body.Close()
			}
		}
		go send("POST", "/v1/passthroughs", fmt.Sprintf(`{"port": 3000, "requester": "claude-code", "session": %q}`, session))
		go send("DELETE", "/v1/passthroughs?session="+session, "")
		wg.Wait()
		// One that the DELETE missed is withdrawn now.
		status(t, e.remote, "DELETE", "/v1/passthroughs?session="+session)
	}
	e.r.mu.Lock()
	defer e.r.mu.Unlock()
	if len(e.r.endedFirst) != 0 {
		t.Fatalf("Ended before the review for %v", e.r.endedFirst)
	}
	if len(e.r.passes) == 0 {
		t.Fatal("no request was reviewed")
	}
	for _, p := range e.r.passes {
		n := 0
		for _, l := range e.r.log {
			if l == "ended "+p.ID {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s ended %d times", p.ID, n)
		}
	}
}

// pageEnv is a server with every call, whose page is at page.
type pageEnv struct {
	s      *Server
	remote string // apollo's socket
}

func newPageServer(t *testing.T, page string) *pageEnv {
	t.Helper()
	rl := &fakeReloader{}
	rl.answer(waiting, nil)
	projects := append(append([]Project(nil), apollo...), Project{Name: "my app", Path: "/srv/my app", State: "running"})
	s := New(Options{
		ID: "laptop-id", Laptop: "dev@laptop", Source: fakeSource{"apollo": projects}, Reload: rl,
		Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}},
		Shower: &fakeShower{served: map[[2]string]string{}}, DocGrants: newFakeDocGrants(),
		PageURL: func() string { return page },
	})
	s.SetReviewer(&fakeReviewer{})
	sock := filepath.Join(shortDir(t), "apollo.sock")
	if err := s.Listen("apollo", sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &pageEnv{s: s, remote: sock}
}

func TestApproveURL(t *testing.T) {
	const page = "https://nearhost.localhost:6327"
	e := newPageServer(t, page)

	st, out := call(t, e.remote, "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`)
	if st != 202 || out["approve_url"] != page+"/approve/"+out["id"].(string) {
		t.Fatalf("passthrough: %d %v", st, out)
	}
	// Asking again for the same request gives the same link.
	if st, again := call(t, e.remote, "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`); st != 202 || again["approve_url"] != out["approve_url"] {
		t.Fatalf("passthrough again: %d %v", st, again)
	}

	st, out = call(t, e.remote, "POST", "/v1/documents", docBody("plan.md", "claude-code", ""))
	if st != 202 || out["approve_url"] != page+"/approve/"+out["id"].(string) {
		t.Fatalf("document: %d %v", st, out)
	}
	if st, again := call(t, e.remote, "POST", "/v1/documents", docBody("plan.md", "claude-code", "")); st != 202 || again["approve_url"] != out["approve_url"] {
		t.Fatalf("document again: %d %v", st, again)
	}

	st, out = call(t, e.remote, "POST", "/v1/proposals", `{"path": "/srv/new"}`)
	if st != 202 || out["approve_url"] != page+"/approve/"+out["id"].(string) || out["advice"] != "Run nearhost on dev@laptop to review it." {
		t.Fatalf("proposal: %d %v", st, out)
	}

	st, out = call(t, e.remote, "POST", "/v1/projects/reload", `{"path": "/srv/my app"}`)
	if st != 202 || out["approve_url"] != page+"/change/my%20app" {
		t.Fatalf("reload: %d %v", st, out)
	}

	// With no page, there is no link.
	e = newPageServer(t, "")
	for _, c := range [][2]string{
		{"/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`},
		{"/v1/documents", docBody("plan.md", "claude-code", "")},
		{"/v1/proposals", `{"path": "/srv/new"}`},
		{"/v1/projects/reload", `{"path": "/srv/my app"}`},
	} {
		st, out := call(t, e.remote, "POST", c[0], c[1])
		_, url := out["approve_url"]
		_, advice := out["advice"]
		if st != 202 || url || advice {
			t.Fatalf("%s with no page: %d %v", c[0], st, out)
		}
	}
}

// noReviewerCopy is the 409 for a request that needs an answer when nothing
// can ask the user, on the test servers' laptop.
const noReviewerCopy = "Nearhost on dev@laptop has no page or window to ask you in; open nearhost there, then ask again."

func TestNoReviewerCopy(t *testing.T) {
	const want = noReviewerCopy
	_, _, _, _, remote := newServer(t, false)
	if st, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app"}`); st != 409 || out["error"] != want {
		t.Fatalf("proposal: %d %v", st, out)
	}
	_, _, rl, _, sock := reloadServer(t, false)
	rl.answer(waiting, nil)
	if st, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`); st != 409 || out["error"] != want ||
		fmt.Sprint(out["items"]) != "[app.example.com docker_clients]" {
		t.Fatalf("reload: %d %v", st, out)
	}
	p := newPassServer(t, false)
	if st, out := p.ask(t, `{"port": 3000, "requester": "claude-code"}`); st != 409 || out["error"] != want {
		t.Fatalf("passthrough: %d %v", st, out)
	}
	d := newDocServer(t, false)
	if st, out := d.ask(t, docBody("plan.md", "claude-code", "")); st != 409 || out["error"] != want {
		t.Fatalf("document: %d %v", st, out)
	}
}
