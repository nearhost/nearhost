package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// rawResponse asks the server at dir for method path and returns the whole
// response, headers included.
func rawResponse(t *testing.T, c *Client, method, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, "http://nearhost"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp, body
}

// A failure the server itself answers is problem details, with the old
// error key first for clients that read only that.
func TestFailIsProblemJSON(t *testing.T) {
	c := dial(t, serve(t, newFake()))
	resp, body := rawResponse(t, c, http.MethodGet, "/v1/x")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if !strings.HasPrefix(string(body), `{"error":"no such route: /v1/x"`) {
		t.Errorf("body = %s, want it to start with the error", body)
	}
	var p struct {
		Code   string `json:"code"`
		Title  string `json:"title"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Code == "" || p.Title != "no such route: /v1/x" || p.Status != http.StatusNotFound {
		t.Errorf("problem = %+v, want a code, the title and status 404", p)
	}
}

// portInUse is an action's error as the engine builds one.
func portInUse() *report.Message {
	return report.Errorf(report.PortInUse, "Port 8080 is in use.").
		Because("Another program listens on it.").
		Step("See what listens on it:", "lsof -i :8080").
		With("port", 8080)
}

// An action's message reaches the client whole, and the marks the client
// branches on survive.
func TestActionErrorRoundTrips(t *testing.T) {
	b := newActionFake()
	c := dial(t, serve(t, b))
	ctx := context.Background()

	b.err = fmt.Errorf("start web: %w", portInUse())
	code, _, err := c.send(ctx, http.MethodPost, "/v1/projects/shop/start", nil, true)
	if err != nil || code != http.StatusConflict {
		t.Fatalf("start = %d, %v, want port.in-use's 409", code, err)
	}
	err = c.StartProject(ctx, "shop")
	m, ok := report.As(err)
	if !ok {
		t.Fatalf("error = %#v, want a message", err)
	}
	want := portInUse()
	if m.Code != want.Code || m.Title != want.Title || m.Why != want.Why || m.Level != report.Error ||
		!reflect.DeepEqual(m.Fix, want.Fix) || fmt.Sprint(m.Data["port"]) != "8080" {
		t.Errorf("message = %+v, want %+v", m, want)
	}
	if m.Context != "start web" {
		t.Errorf("context = %q, want the core's wrapper, start web", m.Context)
	}
	if errors.Is(err, ErrRequestEnded) || errors.Is(err, ErrNotFound) {
		t.Errorf("a port error is marked as ended or not found: %v", err)
	}

	b.err = errEnded
	err = c.AnswerPassthrough(ctx, "r1", "yes")
	if !errors.Is(err, ErrRequestEnded) || err.Error() != errEnded.Error() {
		t.Errorf("ended = %v, want ErrRequestEnded in the core's words", err)
	}
	if _, ok := report.As(err); !ok {
		t.Errorf("ended = %#v, want a message", err)
	}
	code, _, _ = c.send(ctx, http.MethodPost, "/v1/passthroughs/requests/r1/answer", []byte(`{"answer":"yes"}`), true)
	if code != http.StatusConflict {
		t.Errorf("ended answers %d, want 409", code)
	}

	b.err = fmt.Errorf("%w: %w", errEnded, ErrAllowNotSaved)
	err = c.AnswerDocument(ctx, "r2", "always")
	if !errors.Is(err, ErrRequestEnded) || !errors.Is(err, ErrAllowNotSaved) {
		t.Errorf("ended, not saved = %v, want both marks", err)
	}

	b.err = fmt.Errorf("gone: %w", ErrNotFound)
	if err := c.Recheck(ctx, "shop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("not found = %v, want ErrNotFound", err)
	}
	if _, ok := report.As(err); !ok {
		t.Errorf("not found = %#v, want a message", err)
	}
	// A message whose own status is 404 is not the core's not found.
	b.err = report.Errorf(report.AddUnknownServer, "No server is called mini.")
	if err := c.Recheck(ctx, "shop"); errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server = %v, marked as not found", err)
	}
}

// unreachable is the problem of a server that cannot be reached.
func unreachable() *report.Message {
	return report.Errorf(report.SSHUnreachable, "ssh: connect to host leto: Connection refused").
		Step("Connect again:", "nearhost").
		With("server", "leto")
}

// The state and the status carry each problem as a message beside its text.
func TestStateCarriesProblems(t *testing.T) {
	b := newFake()
	p := unreachable()
	b.status.Servers = []ServerInfo{{Name: "leto", State: "reconnecting", Message: p.Title, Problem: p}}
	b.status.Projects = []ProjectInfo{{Name: "blog", State: "error", Message: p.Title, Problem: p}}
	b.state = State{
		Servers: map[string]ServerState{"leto": {
			Health:  engine.ServerHealth{Health: transport.Health{Status: transport.StatusReconnecting, LastError: p.Title, Problem: p}},
			Status:  transport.StatusReconnecting,
			Message: p.Title,
			Problem: p,
		}},
		GrantsError:   "Grants are off.",
		GrantsProblem: report.Errorf(report.AgentsGrantsOff, "Grants are off."),
	}
	c := dial(t, serve(t, b))
	for _, path := range []string{"/v1/status", "/v1/state"} {
		_, body := rawResponse(t, c, http.MethodGet, path)
		if !strings.Contains(string(body), `"problem":{"code":"ssh.unreachable"`) {
			t.Errorf("%s = %s, want the server's problem", path, body)
		}
		if strings.Contains(string(body), `"problem":{"error"`) {
			t.Errorf("%s: a nested problem has an error key", path)
		}
	}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Servers[0]; s.Problem == nil || s.Problem.Code != report.SSHUnreachable || s.Message != s.Problem.Title {
		t.Errorf("server = %+v, want its problem titled as its message", s)
	}
	if pr := st.Projects[0]; pr.Problem == nil || pr.Problem.Code != report.SSHUnreachable || pr.Message != pr.Problem.Title {
		t.Errorf("project = %+v, want its problem titled as its message", pr)
	}
	state, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := state.Servers["leto"]
	if s.Problem == nil || s.Problem.Code != report.SSHUnreachable || s.Message != s.Problem.Title ||
		!reflect.DeepEqual(s.Problem.Fix, p.Fix) || s.Health.Problem == nil {
		t.Errorf("server state = %+v, want its problem", s)
	}
	if state.GrantsProblem == nil || state.GrantsProblem.Code != report.AgentsGrantsOff {
		t.Errorf("GrantsProblem = %+v", state.GrantsProblem)
	}
}

// An event that carries a warning brings it to the client whole.
func TestEventsCarryReportsOnTheStream(t *testing.T) {
	hub, c := serveHub(t, newFake())
	ch, _, _ := openEvents(t, c, 0)
	w := report.Warnf(report.DockerUnreachable, "Docker did not answer.").
		Step("Start Docker:", "open -a Docker").
		With("project", "shop")
	hub.Publish(Envelope{Kind: KindEvent, Event: &engine.Event{
		Time: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC), Project: "shop", Message: w.Title, Report: w,
	}})
	e := next(t, ch)
	if e.Event == nil || e.Event.Report == nil {
		t.Fatalf("envelope = %+v, want an event with its report", e)
	}
	got := e.Event.Report
	if got.Code != w.Code || got.Level != report.Warning || got.Title != w.Title ||
		!reflect.DeepEqual(got.Fix, w.Fix) || got.Data["project"] != "shop" {
		t.Errorf("report = %+v, want %+v", got, w)
	}
}

// Actions that answer 204 keep doing so, with no body.
func TestNoContentActionsStay204(t *testing.T) {
	c := dial(t, serve(t, newActionFake()))
	for _, path := range []string{"/v1/projects/shop/start", "/v1/projects/shop/stop", "/v1/config/reload"} {
		code, body, err := c.send(context.Background(), http.MethodPost, path, nil, true)
		if err != nil || code != http.StatusNoContent || len(body) != 0 {
			t.Errorf("%s = %d %q, %v, want 204 with no body", path, code, body, err)
		}
	}
}

// A client of today, which reads only the error key, reads the title.
func TestOldClientReadsError(t *testing.T) {
	b := newActionFake()
	b.err = portInUse()
	c := dial(t, serve(t, b))
	_, body, err := c.send(context.Background(), http.MethodPost, "/v1/projects/shop/start", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var old errorBody
	if err := json.Unmarshal(body, &old); err != nil || old.Error != portInUse().Title {
		t.Errorf("errorBody = %+v, %v, want the title", old, err)
	}
	var oldAction actionErrorBody
	if err := json.Unmarshal(body, &oldAction); err != nil || oldAction.Error != portInUse().Title || oldAction.Ended {
		t.Errorf("actionErrorBody = %+v, %v, want the title", oldAction, err)
	}
}

// A body from an older core, or no body, still decodes.
func TestDecodeProblemOfOlderCores(t *testing.T) {
	m := decodeProblem([]byte(`{"error":"the request has ended","ended":true}`))
	if m == nil || m.Title != "the request has ended" || m.Level != report.Error || m.Code != report.Unexpected {
		t.Errorf("old body = %+v", m)
	}
	if m := decodeProblem([]byte("not json")); m != nil {
		t.Errorf("not json = %+v, want nil", m)
	}
	if m := decodeProblem([]byte(`{}`)); m != nil {
		t.Errorf("empty = %+v, want nil", m)
	}
	if err := actionError(http.StatusNotImplemented, []byte(`{"error":"this Nearhost cannot do that yet"}`)); !errors.Is(err, ErrUnimplemented) {
		t.Errorf("old 501 = %v, want ErrUnimplemented", err)
	}
	if err := actionError(http.StatusNotFound, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("bare 404 = %v, want ErrNotFound", err)
	}
	err := answerError(http.StatusNotFound, []byte(`{"error":"no such route: /v2"}`))
	if m, ok := report.As(err); !ok || m.Title != "no such route: /v2" {
		t.Errorf("answerError = %#v, want the decoded message", err)
	}
}
