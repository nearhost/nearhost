package page

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

func passApproval(id string, always bool) control.Approval {
	return control.Approval{ID: id, Pass: &agentapi.PassRequest{
		ID: id, Origin: "leto", Requester: "claude-code", Label: "vite", Remote: 5173, CanAlways: always,
	}}
}

func docApproval(id string) control.Approval {
	return control.Approval{ID: id, Doc: &agentapi.DocRequest{
		ID: id, Origin: config.LocalServer, Requester: "codex", Root: "/home/dev/app", Doc: "docs/plan.md", CanAlways: true,
	}}
}

func propApproval(id string) control.Approval {
	return control.Approval{ID: id, Proposal: &agentapi.Proposal{ID: id, Origin: "leto", Path: "/srv/shop", Name: "shop", Web: 8000}}
}

// answer posts answer to the approval id, with the form token.
func answer(id, answer string) url.Values {
	return url.Values{"csrf": {testToken}, "answer": {answer}}
}

func TestApprovalViews(t *testing.T) {
	taken := passApproval("p3", false)
	taken.Pass.LocalTaken = true
	for _, tc := range []struct {
		name    string
		a       control.Approval
		buttons []string
		says    []string
	}{
		{"passthrough with always", passApproval("p1", true), []string{"Yes", "No", "Always allow"},
			[]string{"Claude Code on leto wants to open port 5173 on this laptop (vite).", "It opens on port 5173 here."}},
		{"passthrough without always", passApproval("p2", false), []string{"Yes", "No"}, []string{"Claude Code on leto"}},
		{"passthrough on a taken port", taken, []string{"Yes", "No"}, []string{"Port 5173 is in use here, so it will open on another port."}},
		{"document", docApproval("d1"), []string{"Yes", "No", "Always allow"},
			[]string{"Codex on this machine wants to show docs/plan.md.", "Nearhost will serve files in /home/dev/app, except dotfiles."}},
		{"proposal", propApproval("x"), []string{"No"},
			[]string{"on leto", "/srv/shop", "shop", "8000", "Run nearhost on this machine to review it."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(newPage(newCore(tc.a)), "GET", "/approve/"+tc.a.ID, nil)
			body := bodyOf(t, resp)
			if resp.StatusCode != 200 {
				t.Fatalf("status %d:\n%s", resp.StatusCode, body)
			}
			if got := buttons(body); !slices.Equal(got, tc.buttons) {
				t.Errorf("buttons %q, want %q", got, tc.buttons)
			}
			if !eachFormHasToken(body) {
				t.Errorf("a form lacks the token:\n%s", body)
			}
			if !strings.Contains(body, `action="/approve/`+tc.a.ID+`"`) {
				t.Errorf("the forms do not post to the approval:\n%s", body)
			}
			for _, s := range tc.says {
				if !strings.Contains(body, s) {
					t.Errorf("the view lacks %q:\n%s", s, body)
				}
			}
		})
	}
}

func TestAnswerYes(t *testing.T) {
	c := newCore(passApproval("p1", true))
	resp := do(newPage(c), "POST", "/approve/p1", answer("p1", "yes"))
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/approve/p1?answered=yes" {
		t.Fatalf("POST: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := c.called(); !slices.Equal(got, []string{"AnswerPassthrough p1 yes"}) {
		t.Errorf("calls %q", got)
	}
	// While the request still waits, answered shows it, not an answer.
	if body := bodyOf(t, do(newPage(c), "GET", "/approve/p1?answered=yes", nil)); strings.Contains(body, "Answered") || len(buttons(body)) != 3 {
		t.Errorf("answered while the request waits:\n%s", body)
	}
	// The request has gone from the queue by the time the browser follows
	// the redirect.
	c.state.Approvals = nil
	body := bodyOf(t, do(newPage(c), "GET", "/approve/p1?answered=yes", nil))
	if !strings.Contains(body, "Answered: Yes.") || !strings.Contains(body, `href="/"`) {
		t.Errorf("after the answer:\n%s", body)
	}
}

func TestAnswerDocumentAlways(t *testing.T) {
	c := newCore(docApproval("d1"))
	resp := do(newPage(c), "POST", "/approve/d1", answer("d1", "always"))
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/approve/d1?answered=always" {
		t.Fatalf("POST: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := c.called(); !slices.Equal(got, []string{"AnswerDocument d1 always"}) {
		t.Errorf("calls %q", got)
	}
	c.state.Approvals = nil
	if body := bodyOf(t, do(newPage(c), "GET", "/approve/d1?answered=always", nil)); !strings.Contains(body, "Answered: Always allow.") {
		t.Errorf("after the answer:\n%s", body)
	}
}

func TestAnswerRefusesWhatIsNotOffered(t *testing.T) {
	c := newCore(passApproval("p2", false))
	for _, a := range []string{"always", "maybe", ""} {
		if resp := do(newPage(c), "POST", "/approve/p2", answer("p2", a)); resp.StatusCode != 400 {
			t.Errorf("answer %q: %d", a, resp.StatusCode)
		}
	}
	if got := c.called(); len(got) != 0 {
		t.Errorf("calls %q", got)
	}
}

func TestProposalOnlyNo(t *testing.T) {
	c := newCore(propApproval("x"))
	if resp := do(newPage(c), "POST", "/approve/x", answer("x", "yes")); resp.StatusCode != 400 {
		t.Errorf("yes on a proposal: %d", resp.StatusCode)
	}
	if got := c.called(); len(got) != 0 {
		t.Fatalf("yes on a proposal called %q", got)
	}
	resp := do(newPage(c), "POST", "/approve/x", answer("x", "no"))
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/approve/x?answered=no" {
		t.Fatalf("no: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := c.called(); !slices.Equal(got, []string{"DeclineProposal x declined on Nearhost's page"}) {
		t.Errorf("calls %q", got)
	}
	c.state.Approvals = nil
	if body := bodyOf(t, do(newPage(c), "GET", "/approve/x?answered=no", nil)); !strings.Contains(body, "Answered: No.") {
		t.Errorf("after the decline:\n%s", body)
	}
}

func TestAnsweredElsewhere(t *testing.T) {
	const gone = "This request has been answered or has ended."
	c := newCore(passApproval("p1", true))
	c.err = fmt.Errorf("answer: %w", control.ErrRequestEnded)
	resp := do(newPage(c), "POST", "/approve/p1", answer("p1", "yes"))
	if body := bodyOf(t, resp); resp.StatusCode != 409 || !strings.Contains(body, gone) {
		t.Errorf("answered elsewhere: %d\n%s", resp.StatusCode, body)
	}
	resp = do(newPage(c), "GET", "/approve/nope", nil)
	body := bodyOf(t, resp)
	if resp.StatusCode != 404 || !strings.Contains(body, gone) || strings.Contains(body, "<form") {
		t.Errorf("an id not pending: %d\n%s", resp.StatusCode, body)
	}
	// One withdrawn while the view showed it gets the same answer to its
	// POST, and nothing is called.
	calls := len(c.called())
	resp = do(newPage(c), "POST", "/approve/nope", answer("nope", "yes"))
	if body := bodyOf(t, resp); resp.StatusCode != 409 || !strings.Contains(body, gone) || len(c.called()) != calls {
		t.Errorf("POST to an id not pending: %d\n%s", resp.StatusCode, body)
	}
}

// An answer while another still opens the request has lost, as one after
// it has: 409, and nothing left to answer.
func TestAnswerWhileAnotherOpens(t *testing.T) {
	c := newCore(passApproval("p1", true))
	c.err = fmt.Errorf("the request for port 5173 is %w", agentapi.ErrBeingAnswered)
	resp := do(newPage(c), "POST", "/approve/p1", answer("p1", "yes"))
	body := bodyOf(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "This request has been answered or has ended.") {
		t.Errorf("an answer while another opens: %d\n%s", resp.StatusCode, body)
	}
	if got := buttons(body); len(got) != 0 || strings.Contains(body, "<form") {
		t.Errorf("an answer while another opens offers %q:\n%s", got, body)
	}
}

func TestAnswerErrorShown(t *testing.T) {
	c := newCore(passApproval("p1", true))
	c.err = errors.New("port 3000 on leto: not connected")
	resp := do(newPage(c), "POST", "/approve/p1", answer("p1", "yes"))
	body := bodyOf(t, resp)
	if resp.StatusCode != 502 || !strings.Contains(body, "port 3000 on leto: not connected") || !strings.Contains(body, "Claude Code on leto") {
		t.Errorf("a failed answer: %d\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "<html") {
		t.Errorf("a failed answer is not a page:\n%s", body)
	}
	if got := buttons(body); len(got) != 3 {
		t.Errorf("a request that still waits offers %q", got)
	}
}

// A Yes that ended the request, because it could not be opened, says why
// with nothing left to answer, rather than that it was answered elsewhere.
func TestAnswerNotOpenedShowsWhy(t *testing.T) {
	c := &endingCore{newCore(passApproval("p1", true))}
	c.err = errors.Join(errors.New("apollo: not connected; run nearhost doctor"), agentapi.ErrNotOpened, control.ErrRequestEnded)
	resp := do(newPage(c), "POST", "/approve/p1", answer("p1", "yes"))
	body := bodyOf(t, resp)
	if resp.StatusCode != 502 || !strings.Contains(body, "apollo: not connected; run nearhost doctor") || !strings.Contains(body, "Claude Code on leto") {
		t.Errorf("a failed open: %d\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "This request has been answered or has ended.") {
		t.Errorf("a failed open reads as answered elsewhere:\n%s", body)
	}
	if got := buttons(body); len(got) != 0 || strings.Contains(body, "<form") {
		t.Errorf("a request that waits no longer offers %q:\n%s", got, body)
	}
}

// An answer that failed for a request that waits no longer shows its error
// with nothing left to answer.
func TestAnswerErrorWithoutButtons(t *testing.T) {
	c := &endingCore{newCore(passApproval("p1", true))}
	c.err = errors.New("Always allow was not saved: disk full")
	resp := do(newPage(c), "POST", "/approve/p1", answer("p1", "always"))
	body := bodyOf(t, resp)
	if resp.StatusCode != 502 || !strings.Contains(body, "disk full") || !strings.Contains(body, "Claude Code on leto") {
		t.Errorf("a failed answer: %d\n%s", resp.StatusCode, body)
	}
	if got := buttons(body); len(got) != 0 || strings.Contains(body, "<form") {
		t.Errorf("a request that waits no longer offers %q:\n%s", got, body)
	}
}

// endingCore is a fakeCore whose requests stop waiting once answered.
type endingCore struct{ *fakeCore }

func (c *endingCore) AnswerPassthrough(ctx context.Context, id, answer string) error {
	err := c.fakeCore.AnswerPassthrough(ctx, id, answer)
	c.state.Approvals = nil
	return err
}

// slowCore is a fakeCore whose answers block until release closes, then
// record their context's error and deadline.
type slowCore struct {
	*fakeCore
	started, release chan struct{}
	ctxErr           error
	deadline         time.Time
}

func (c *slowCore) wait(ctx context.Context) {
	c.started <- struct{}{}
	<-c.release
	c.ctxErr = ctx.Err()
	c.deadline, _ = ctx.Deadline()
}

func (c *slowCore) AnswerPassthrough(ctx context.Context, id, answer string) error {
	c.wait(ctx)
	return c.fakeCore.AnswerPassthrough(ctx, id, answer)
}

func (c *slowCore) AnswerDocument(ctx context.Context, id, answer string) error {
	c.wait(ctx)
	return c.fakeCore.AnswerDocument(ctx, id, answer)
}

// A browser that goes away while the answer opens does not cancel it: the
// open runs on, bounded by the agent API's open time.
func TestAnswerOutlivesTheBrowser(t *testing.T) {
	for _, a := range []control.Approval{passApproval("p1", true), docApproval("p1")} {
		c := &slowCore{fakeCore: newCore(a), started: make(chan struct{}, 1), release: make(chan struct{})}
		ctx, hangUp := context.WithCancel(context.Background())
		r := httptest.NewRequestWithContext(ctx, "POST", "/approve/p1", strings.NewReader(answer("p1", "yes").Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		done := make(chan struct{})
		go func() {
			defer close(done)
			newPage(c).ServeHTTP(httptest.NewRecorder(), r)
		}()
		<-c.started
		hangUp()
		close(c.release)
		<-done
		if c.ctxErr != nil {
			t.Errorf("the hang-up cancelled the answer: %v", c.ctxErr)
		}
		if left := time.Until(c.deadline); left <= 0 || left > agentapi.OpenTimeout {
			t.Errorf("the answer's deadline is %v away, want within %v", left, agentapi.OpenTimeout)
		}
		if got := c.called(); len(got) != 1 {
			t.Errorf("calls %q", got)
		}
	}
}

// changeCore is a core whose project web's .nearhostrc waits on a change:
// it serves web.test on port 80 now, and the change moves it to 8080 and
// asks for api.example.com there, and for docker_clients.
func changeCore() *fakeCore {
	c := newCore()
	web := config.Project{Name: "web", Alias: "web.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"web.test"}}}}
	data, err := config.Marshal(&config.Config{Projects: []config.Project{web}})
	if err != nil {
		panic(err)
	}
	c.state.Config = string(data)
	next := web
	next.Routes = []config.Route{{Port: 8080, Role: config.RoleWeb, Hosts: []string{"web.test", "api.example.com"}}}
	c.state.Projects["web"] = engine.ProjectStatus{Pending: &engine.Change{SHA256: "sha1", Project: next, Items: []nearhostrc.Item{
		{Port: 8080, Host: "api.example.com", Why: nearhostrc.WhyRealDomain},
		{Option: nearhostrc.OptionDockerClients, Why: "opens the project to containers"},
	}}}
	return c
}

func TestChangeApprove(t *testing.T) {
	c := changeCore()
	resp := do(newPage(c), "GET", "/change/web", nil)
	body := bodyOf(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("GET: %d\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		"api.example.com on port 8080 (a real domain)", "Serving it here hides the real site on this machine.",
		"docker_clients (opens the project to containers)", `name="sha" value="sha1"`,
		"It also moves or removes what is served now:", "web.test port 80 → 8080",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the change lacks %q:\n%s", want, body)
		}
	}
	if got := buttons(body); !slices.Equal(got, []string{"Approve", "Decline"}) {
		t.Errorf("buttons %q", got)
	}
	if !eachFormHasToken(body) {
		t.Errorf("a form lacks the token:\n%s", body)
	}
	resp = do(newPage(c), "POST", "/change/web", url.Values{"csrf": {testToken}, "answer": {"approve"}, "sha": {"sha1"}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/change/web?answered=approve&sha=sha1" {
		t.Fatalf("approve: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := c.called(); !slices.Equal(got, []string{"AcceptChange web sha1"}) {
		t.Errorf("calls %q", got)
	}
	// The approval is saved, but the change waits until the config is read
	// again: the answer shows for the change approved, and for no other.
	if body := bodyOf(t, do(newPage(c), "GET", "/change/web?answered=approve&sha=sha1", nil)); !strings.Contains(body, "Answered: Approved.") || buttons(body) != nil {
		t.Errorf("approved while the change is applied:\n%s", body)
	}
	for _, q := range []string{"answered=approve", "answered=approve&sha=sha0", "answered=decline&sha=sha1"} {
		if body := bodyOf(t, do(newPage(c), "GET", "/change/web?"+q, nil)); strings.Contains(body, "Answered") || !strings.Contains(body, "api.example.com") {
			t.Errorf("%s answered while the change waits:\n%s", q, body)
		}
	}
	c.state.Projects["web"] = engine.ProjectStatus{}
	if body := bodyOf(t, do(newPage(c), "GET", "/change/web?answered=approve", nil)); !strings.Contains(body, "Answered: Approved.") {
		t.Errorf("after the answer:\n%s", body)
	}
}

func TestChangeDecline(t *testing.T) {
	c := changeCore()
	resp := do(newPage(c), "POST", "/change/web", url.Values{"csrf": {testToken}, "answer": {"decline"}, "sha": {"sha1"}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/change/web?answered=decline" {
		t.Fatalf("decline: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := c.called(); !slices.Equal(got, []string{"DeclineChange web sha1"}) {
		t.Errorf("calls %q", got)
	}
}

func TestChangeStale(t *testing.T) {
	c := changeCore()
	c.err = errors.New("the .nearhostrc changed again; review it again")
	resp := do(newPage(c), "POST", "/change/web", url.Values{"csrf": {testToken}, "answer": {"approve"}, "sha": {"sha0"}})
	if body := bodyOf(t, resp); resp.StatusCode != 409 || !strings.Contains(body, "changed again; review it again") {
		t.Errorf("a stale approve: %d\n%s", resp.StatusCode, body)
	}
	// A stale decline is not passed on: it would decline nothing.
	calls := len(c.called())
	resp = do(newPage(c), "POST", "/change/web", url.Values{"csrf": {testToken}, "answer": {"decline"}, "sha": {"sha0"}})
	if body := bodyOf(t, resp); resp.StatusCode != 409 || !strings.Contains(body, "changed again; review it again") || len(c.called()) != calls {
		t.Errorf("a stale decline: %d\n%s", resp.StatusCode, body)
	}
	if resp := do(newPage(c), "POST", "/change/web", url.Values{"csrf": {testToken}, "answer": {"maybe"}, "sha": {"sha1"}}); resp.StatusCode != 400 {
		t.Errorf("a bad answer: %d", resp.StatusCode)
	}
	if resp := do(newPage(c), "GET", "/change/api", nil); resp.StatusCode != 404 {
		t.Errorf("a project with no change: %d", resp.StatusCode)
	}
}

// An answer to a change that no longer waits, as one answered in another
// window, says it has ended, not that the file changed again.
func TestChangeAnsweredElsewhere(t *testing.T) {
	for _, ans := range []string{"approve", "decline"} {
		c := changeCore()
		c.state.Projects["web"] = engine.ProjectStatus{}
		c.err = errors.New("the .nearhostrc changed again; review it again")
		resp := do(newPage(c), "POST", "/change/web", url.Values{"csrf": {testToken}, "answer": {ans}, "sha": {"sha1"}})
		body := bodyOf(t, resp)
		if resp.StatusCode != 409 || !strings.Contains(body, "This request has been answered or has ended.") || strings.Contains(body, "changed again") || buttons(body) != nil {
			t.Errorf("%s: %d\n%s", ans, resp.StatusCode, body)
		}
	}
}
