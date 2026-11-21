package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/proxy"
)

// errEnded is the marker the fake's ended errors wrap.
var errEnded = fmt.Errorf("the agent gave up: %w", ErrRequestEnded)

// actionFake is a Backend whose actions record what they are asked and return
// err, when they can fail.
type actionFake struct {
	*fakeBackend
	amu  sync.Mutex
	log  []string
	err  error
	slow time.Duration // how long AnswerPassthrough takes
}

func (f *actionFake) record(format string, args ...any) error {
	f.amu.Lock()
	defer f.amu.Unlock()
	f.log = append(f.log, fmt.Sprintf(format, args...))
	return f.err
}

func (f *actionFake) recorded() []string {
	f.amu.Lock()
	defer f.amu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *actionFake) StartProject(_ context.Context, n string) error {
	return f.record("StartProject %q", n)
}
func (f *actionFake) StopProject(_ context.Context, n string) error {
	return f.record("StopProject %q", n)
}
func (f *actionFake) Recheck(_ context.Context, n string) error { return f.record("Recheck %q", n) }
func (f *actionFake) AcceptChange(n, sha string) error {
	return f.record("AcceptChange %q %q", n, sha)
}
func (f *actionFake) DeclineChange(n, sha string) { f.record("DeclineChange %q %q", n, sha) }
func (f *actionFake) ConnectServer(_ context.Context, n string) error {
	return f.record("ConnectServer %q", n)
}
func (f *actionFake) DisconnectServer(_ context.Context, n string) error {
	return f.record("DisconnectServer %q", n)
}
func (f *actionFake) ReconnectServer(_ context.Context, n string) error {
	return f.record("ReconnectServer %q", n)
}
func (f *actionFake) Reload(context.Context) error { return f.record("Reload") }
func (f *actionFake) AcceptProposal(id, n string)  { f.record("AcceptProposal %q %q", id, n) }
func (f *actionFake) DeclineProposal(id, r string) { f.record("DeclineProposal %q %q", id, r) }
func (f *actionFake) AnswerPassthrough(_ context.Context, id, a string) error {
	time.Sleep(f.slow)
	return f.record("AnswerPassthrough %q %q", id, a)
}
func (f *actionFake) ClosePassthrough(_ context.Context, id string) error {
	return f.record("ClosePassthrough %q", id)
}
func (f *actionFake) AnswerDocument(_ context.Context, id, a string) error {
	return f.record("AnswerDocument %q %q", id, a)
}
func (f *actionFake) OpenDocument(id string) error    { return f.record("OpenDocument %q", id) }
func (f *actionFake) RemoveDocuments(id string) error { return f.record("RemoveDocuments %q", id) }
func (f *actionFake) RevokeGrant(s, r, k string) error {
	return f.record("RevokeGrant %q %q %q", s, r, k)
}
func (f *actionFake) Captured(id string) (proxy.Exchange, bool) {
	f.record("Captured %q", id)
	if id == "gone" {
		return proxy.Exchange{}, false
	}
	return proxy.Exchange{ID: id, Method: "POST", Host: "shop.test", Status: 201, ReqBody: []byte("hi")}, true
}
func (f *actionFake) Replay(_ context.Context, id string) (int, error) {
	return 202, f.record("Replay %q", id)
}
func (f *actionFake) PageKeyURL() (string, error) {
	return "https://nearhost.localhost/?k=abc", f.record("PageKeyURL")
}

func newActionFake() *actionFake { return &actionFake{fakeBackend: newFake()} }

func TestActionsReachTheBackend(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		want string
		// canFail is whether the backend method returns an error.
		canFail bool
		call    func(c *Client) error
	}{
		{"StartProject", `StartProject "shop"`, true, func(c *Client) error { return c.StartProject(ctx, "shop") }},
		{"StopProject", `StopProject "shop"`, true, func(c *Client) error { return c.StopProject(ctx, "shop") }},
		{"Recheck", `Recheck "shop"`, true, func(c *Client) error { return c.Recheck(ctx, "shop") }},
		{"AcceptChange", `AcceptChange "shop" "abc123"`, true, func(c *Client) error { return c.AcceptChange(ctx, "shop", "abc123") }},
		{"DeclineChange", `DeclineChange "shop" "abc123"`, false, func(c *Client) error { return c.DeclineChange(ctx, "shop", "abc123") }},
		{"ConnectServer", `ConnectServer "leto"`, true, func(c *Client) error { return c.ConnectServer(ctx, "leto") }},
		{"DisconnectServer", `DisconnectServer "leto"`, true, func(c *Client) error { return c.DisconnectServer(ctx, "leto") }},
		{"ReconnectServer", `ReconnectServer "leto"`, true, func(c *Client) error { return c.ReconnectServer(ctx, "leto") }},
		{"Reload", `Reload`, true, func(c *Client) error { return c.Reload(ctx) }},
		{"AcceptProposal", `AcceptProposal "p1" "shop"`, false, func(c *Client) error { return c.AcceptProposal(ctx, "p1", "shop") }},
		{"DeclineProposal", `DeclineProposal "p1" "not mine"`, false, func(c *Client) error { return c.DeclineProposal(ctx, "p1", "not mine") }},
		{"AnswerPassthrough", `AnswerPassthrough "r1" "always"`, true, func(c *Client) error { return c.AnswerPassthrough(ctx, "r1", "always") }},
		{"ClosePassthrough", `ClosePassthrough "t1"`, true, func(c *Client) error { return c.ClosePassthrough(ctx, "t1") }},
		{"AnswerDocument", `AnswerDocument "r2" "yes"`, true, func(c *Client) error { return c.AnswerDocument(ctx, "r2", "yes") }},
		{"OpenDocument", `OpenDocument "d1"`, true, func(c *Client) error { return c.OpenDocument(ctx, "d1") }},
		{"RemoveDocuments", `RemoveDocuments "d1"`, true, func(c *Client) error { return c.RemoveDocuments(ctx, "d1") }},
		{"RevokeGrant", `RevokeGrant "leto" "claude" "document"`, true, func(c *Client) error { return c.RevokeGrant(ctx, "leto", "claude", "document") }},
		{"Replay", `Replay "x1"`, true, func(c *Client) error {
			status, err := c.Replay(ctx, "x1")
			if err == nil && status != 202 {
				return fmt.Errorf("Replay status = %d, want 202", status)
			}
			return err
		}},
		{"PageKeyURL", `PageKeyURL`, true, func(c *Client) error {
			u, err := c.PageKeyURL(ctx)
			if err == nil && u != "https://nearhost.localhost/?k=abc" {
				return fmt.Errorf("PageKeyURL = %q", u)
			}
			return err
		}},
		{"Captured", `Captured "x1"`, false, func(c *Client) error {
			ex, ok := c.Captured(ctx, "x1")
			if !ok || ex.ID != "x1" || ex.Status != 201 || string(ex.ReqBody) != "hi" {
				return fmt.Errorf("Captured = %+v, %v", ex, ok)
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newActionFake()
			c := dial(t, serve(t, b))
			if err := tc.call(c); err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			if got := b.recorded(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("the backend got %q, want [%s]", got, tc.want)
			}
			if !tc.canFail {
				return
			}
			b.err = errors.New("the server is not connected")
			err := tc.call(c)
			if err == nil || err.Error() != b.err.Error() {
				t.Fatalf("the call's error = %v, want %q", err, b.err)
			}
		})
	}
}

func TestAnswerPassthroughEnded(t *testing.T) {
	b := newActionFake()
	b.err = errEnded
	c := dial(t, serve(t, b))
	err := c.AnswerPassthrough(context.Background(), "r1", "yes")
	if !errors.Is(err, ErrRequestEnded) {
		t.Fatalf("error = %v, want ErrRequestEnded", err)
	}
	if err.Error() != errEnded.Error() {
		t.Fatalf("error reads %q, want %q", err, errEnded)
	}
}

func TestAnswerHasNoFiveSecondCap(t *testing.T) {
	old := callTimeout
	callTimeout = 50 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	b := newActionFake()
	b.slow = 100 * time.Millisecond
	c := dial(t, serve(t, b))
	if err := c.AnswerPassthrough(context.Background(), "r1", "yes"); err != nil {
		t.Fatalf("a slow answer failed: %v", err)
	}
	// The routes that do not block keep the cap.
	b.slow = 0
	if err := c.Recheck(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
}

func TestNonBlockingActionsKeepTheCap(t *testing.T) {
	old := callTimeout
	callTimeout = 50 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	hang := &hangingBackend{actionFake: newActionFake(), release: make(chan struct{})}
	t.Cleanup(func() { close(hang.release) })
	c := dial(t, serve(t, hang))
	if err := c.ConnectServer(context.Background(), "leto"); err == nil {
		t.Fatal("a call that hangs came back without an error")
	}
}

type hangingBackend struct {
	*actionFake
	release chan struct{}
}

func (h *hangingBackend) ConnectServer(context.Context, string) error {
	<-h.release
	return nil
}

func TestCapturedGone(t *testing.T) {
	c := dial(t, serve(t, newActionFake()))
	ex, ok := c.Captured(context.Background(), "gone")
	if ok || ex.ID != "" {
		t.Fatalf("Captured = %+v, %v, want the zero Exchange and false", ex, ok)
	}
}

func TestNamesAreEscaped(t *testing.T) {
	b := newActionFake()
	c := dial(t, serve(t, b))
	if err := c.StartProject(context.Background(), "a b/c"); err != nil {
		t.Fatal(err)
	}
	if got := b.recorded(); len(got) != 1 || got[0] != `StartProject "a b/c"` {
		t.Fatalf("the backend got %q", got)
	}
	if esc("a b/c") != url.PathEscape("a b/c") {
		t.Fatal("names are not escaped as paths")
	}
}

func TestUnknownAnswerRefused(t *testing.T) {
	b := newActionFake()
	c := dial(t, serve(t, b))
	for _, path := range []string{"/v1/passthroughs/requests/r1/answer", "/v1/documents/requests/r1/answer"} {
		err := c.act(context.Background(), path, map[string]string{"answer": "maybe"}, nil, true)
		if err == nil || !strings.Contains(err.Error(), "maybe") {
			t.Fatalf("%s: error = %v, want one that names the answer", path, err)
		}
		code, _, rerr := c.send(context.Background(), http.MethodPost, path, []byte(`{"answer":"maybe"}`), true)
		if rerr != nil || code != http.StatusBadRequest {
			t.Fatalf("%s = %d, %v, want 400", path, code, rerr)
		}
	}
	if got := b.recorded(); len(got) != 0 {
		t.Fatalf("the backend was called: %q", got)
	}
}

func TestNotFoundAndMethodsAnswerInJSON(t *testing.T) {
	c := dial(t, serve(t, newActionFake()))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/v1/projects/shop/start", http.StatusMethodNotAllowed},
		{"POST", "/v1/page", http.StatusMethodNotAllowed},
		{"POST", "/v1/captured/x1", http.StatusMethodNotAllowed},
		{"POST", "/v1/projects/shop/explode", http.StatusNotFound},
		{"GET", "/v1/nothing", http.StatusNotFound},
	} {
		code, body, err := c.raw(context.Background(), tc.method, tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if code != tc.want || !jsonHasError(body) {
			t.Errorf("%s %s = %d %q, want %d with a JSON error", tc.method, tc.path, code, body, tc.want)
		}
	}
}

func TestUnimplementedActions(t *testing.T) {
	// fakeBackend leaves the actions to UnimplementedActions.
	c := dial(t, serve(t, newFake()))
	ctx := context.Background()
	const text = "this Nearhost cannot do that yet"
	for name, err := range map[string]error{
		"StartProject":      c.StartProject(ctx, "shop"),
		"AnswerPassthrough": c.AnswerPassthrough(ctx, "r1", "yes"),
		"Reload":            c.Reload(ctx),
		"RevokeGrant":       c.RevokeGrant(ctx, "a", "b", "c"),
	} {
		if err == nil || err.Error() != text {
			t.Errorf("%s: error = %v, want %q", name, err, text)
		}
	}
	code, body, err := c.raw(ctx, http.MethodPost, "/v1/config/reload")
	if err != nil || code != http.StatusNotImplemented || !strings.Contains(string(body), text) {
		t.Fatalf("reload = %d %q, %v, want 501", code, body, err)
	}
	if _, ok := c.Captured(ctx, "x"); ok {
		t.Error("Captured found something")
	}
	if err := c.DeclineProposal(ctx, "p", "no"); err != nil {
		t.Errorf("DeclineProposal: %v", err)
	}
}
