package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/report"
)

// errEnv is a server with every part an agent can call, each a fake, called
// through its handler with the origin set as a socket would set it.
type errEnv struct {
	s      *Server
	rev    *fakeReviewer
	c      *clock
	doc    *fakeDoctor
	rel    *fakeReloader
	pass   *fakePasser
	grants *fakeGrants
	sh     *fakeShower
	dg     *fakeDocGrants
}

func newErrEnv(t *testing.T, review bool) *errEnv {
	t.Helper()
	e := &errEnv{
		rev:    &fakeReviewer{},
		c:      &clock{now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)},
		doc:    &fakeDoctor{},
		rel:    &fakeReloader{},
		pass:   &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}},
		grants: &fakeGrants{tokens: map[[2]string]string{}},
		sh:     &fakeShower{served: map[[2]string]string{}},
		dg:     newFakeDocGrants(),
	}
	e.s = New(Options{
		ID: "laptop-id", Laptop: "dev@laptop", Source: fakeSource{"mini": mini, "apollo": apollo},
		Doctor: e.doc, Reload: e.rel, Passer: e.pass, Grants: e.grants, Shower: e.sh, DocGrants: e.dg, Now: e.c.Now,
	})
	if review {
		e.s.SetReviewer(e.rev)
	}
	t.Cleanup(e.s.Close)
	return e
}

// request is a request from origin, as its socket would make it.
func request(origin, method, target, body string) *http.Request {
	r := httptest.NewRequest(method, "http://nearhost"+target, strings.NewReader(body))
	return r.WithContext(context.WithValue(r.Context(), originKey{}, origin))
}

// do serves a request from origin and returns the reply.
func (e *errEnv) do(origin, method, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.s.handler.ServeHTTP(w, request(origin, method, target, body))
	return w
}

// query is "?" and vals, encoded.
func query(vals ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(vals); i += 2 {
		q.Set(vals[i], vals[i+1])
	}
	return "?" + q.Encode()
}

// errCase is one place the agent API answers with an error: the status and
// the exact `error` text it has always answered with there.
type errCase struct {
	name   string
	run    func(t *testing.T, e *errEnv) *httptest.ResponseRecorder
	review bool
	status int
	text   string
	// retry is the Retry-After header, when the reply has one.
	retry string
}

const noReviewerText = "Nearhost on dev@laptop has no page or window to ask you in; open nearhost there, then ask again."

// passWithToken has requester claude-code hold token on apollo, so its
// passthroughs open without asking.
func (e *errEnv) passWithToken() {
	e.grants.tokens[[2]string{"apollo", "claude-code"}] = "good"
}

// openPass opens a passthrough of port on apollo with a token, in session
// s, and returns its id.
func (e *errEnv) openPass(t *testing.T, port int) string {
	t.Helper()
	e.passWithToken()
	w := e.do("apollo", "POST", "/v1/passthroughs", fmt.Sprintf(`{"port": %d, "requester": "claude-code", "session": "s", "token": "good"}`, port))
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	id, _ := out["id"].(string)
	if w.Code != 201 || id == "" {
		t.Fatalf("open %d: %d %s", port, w.Code, w.Body)
	}
	return id
}

// failCloses has every Close of a forward fail with "link down".
func (e *errEnv) failCloses() {
	e.pass.mu.Lock()
	e.pass.onClose = func(context.Context, int) error { return errors.New("link down") }
	e.pass.mu.Unlock()
}

// closeSessionOnOpen has the next Open close session s first, as if the
// agent's session ended while its passthrough opened.
func (e *errEnv) closeSessionOnOpen() {
	e.pass.mu.Lock()
	e.pass.onOpen = func() {
		e.pass.mu.Lock()
		e.pass.onOpen = nil
		e.pass.mu.Unlock()
		e.do("apollo", "DELETE", "/v1/passthroughs?session=s", "")
	}
	e.pass.mu.Unlock()
}

func post(origin, target, body string) func(*testing.T, *errEnv) *httptest.ResponseRecorder {
	return func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder { return e.do(origin, "POST", target, body) }
}

func get(origin, target string) func(*testing.T, *errEnv) *httptest.ResponseRecorder {
	return func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder { return e.do(origin, "GET", target, "") }
}

// errorCases are every place the agent API answers with an error.
func errorCases() []errCase {
	notJSON := func() string {
		var v proposeRequest
		err := json.NewDecoder(strings.NewReader("nope")).Decode(&v)
		return `the body must be JSON like {"path": "/abs/dir"}: ` + err.Error()
	}()
	long := strings.Repeat("x", 65)
	doc := func(extra string) string { return docBody("a.md", "claude-code", extra) }
	cases := []errCase{
		// handlers.go
		{name: "no such call", run: get("mini", "/v1/nope"), status: 404, text: "no such call; GET /v1 says which API this is"},
		{name: "no token", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			requireToken("secret", e.s.handler).ServeHTTP(w, request("mini", "GET", "/v1", ""))
			return w
		}, status: 401, text: "this request has no valid Nearhost token: update the Nearhost skill on this machine"},
		{name: "no project holds", run: get("mini", "/v1/projects"+query("path", "/home/dev/elsewhere")), status: 404,
			text: "no project holds /home/dev/elsewhere"},
		{name: "path not absolute", run: get("mini", "/v1/projects"+query("path", "rel")), status: 400, text: `path must be absolute, not "rel"`},
		{name: "real not absolute", run: get("mini", "/v1/projects"+query("path", "/home/dev/code/myapp", "real", "rel")), status: 400,
			text: `real must be absolute, not "rel"`},
		{name: "network path", run: get("local", "/v1/projects"+query("path", `\\server\share`)), status: 400,
			text: `path is a network path: network paths (\\server\share) aren't supported yet`},
		{name: "device path", run: get("local", "/v1/projects"+query("path", `\\.\pipe\x`)), status: 400,
			text: `path is a Windows device path (\\.\ or \\?\); send the plain C:\… form`},
		{name: "doctor busy", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.s.mu.Lock()
			e.s.doctoring["mini"] = true
			e.s.mu.Unlock()
			return e.do("mini", "GET", "/v1/doctor"+query("path", "/home/dev/code/myapp"), "")
		}, status: 429, text: "a doctor run is already going for this server"},
		{name: "doctor too soon", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.s.mu.Lock()
			e.s.doctored["mini"] = e.c.Now()
			e.s.mu.Unlock()
			return e.do("mini", "GET", "/v1/doctor"+query("path", "/home/dev/code/myapp"), "")
		}, status: 429, text: "a doctor run for this server started moments ago; ask again in 5 s", retry: "5"},
		{name: "doctor not a directory", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.doc.err = fmt.Errorf("stat: %w", fs.ErrNotExist)
			return e.do("mini", "GET", "/v1/doctor"+query("path", "/home/dev/gone"), "")
		}, status: 404, text: "/home/dev/gone is not a directory on mini"},
		{name: "doctor failed", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.doc.err = errors.New("connect to mini.lan: no route to host")
			return e.do("mini", "GET", "/v1/doctor"+query("path", "/home/dev/gone"), "")
		}, status: 502, text: "connect to mini.lan: no route to host"},
		{name: "body over 4 KiB", run: post("mini", "/v1/proposals", `{"path": "/`+strings.Repeat("x", 5000)+`"}`), status: 400,
			text: "the request is over 4 KiB"},
		{name: "body not JSON", run: post("mini", "/v1/proposals", "nope"), status: 400, text: notJSON},
		{name: "web port", run: post("mini", "/v1/proposals", `{"path": "/home/dev/new", "web": 70000}`), status: 400,
			text: "web must be a port between 1 and 65535, not 70000"},
		{name: "name", run: post("mini", "/v1/proposals", `{"path": "/home/dev/new", "name": "`+long+`"}`), status: 400,
			text: "name must be at most 64 printable characters on one line"},
		{name: "proposal without reviewer", run: post("mini", "/v1/proposals", `{"path": "/home/dev/new"}`), status: 409, text: noReviewerText},
		{name: "proposals waiting", review: true, run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			for range 3 {
				e.do("mini", "POST", "/v1/proposals", `{"path": "/home/dev/new"}`)
			}
			return e.do("mini", "POST", "/v1/proposals", `{"path": "/home/dev/new"}`)
		}, status: 429, text: "3 proposals are already waiting for review; wait for them first"},
		{name: "reload of no project", run: post("mini", "/v1/projects/reload", `{"path": "/home/dev/other"}`), status: 404,
			text: "no project holds /home/dev/other; add it first"},
		{name: "reload failed", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.rel.answer(Reloaded{}, errors.New(`server "mini" is not connected`))
			return e.do("mini", "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
		}, status: 502, text: `server "mini" is not connected`},
		{name: "reload problem", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.rel.answer(Reloaded{Problem: `.nearhostrc: line 3: unknown key "hsts"`}, nil)
			return e.do("mini", "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
		}, status: 422, text: `.nearhostrc: line 3: unknown key "hsts"`},
		{name: "reload without reviewer", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.rel.answer(Reloaded{Items: []string{"web 8080"}, SHA256: "abc"}, nil)
			return e.do("mini", "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
		}, status: 409, text: noReviewerText},
		{name: "wait", run: get("mini", "/v1/proposals/abc?wait=soon"), status: 400, text: `wait must be a number of seconds, not "soon"`},
		{name: "no such proposal", run: get("mini", "/v1/proposals/abc"), status: 404, text: "no such proposal; it may have ended over 10 minutes ago"},

		// passthrough_handlers.go
		{name: "passthrough of the laptop", run: post("local", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`), status: 400,
			text: "a passthrough forwards a server's port to dev@laptop; this socket is the laptop's own"},
		{name: "passthrough port", run: post("apollo", "/v1/passthroughs", `{"port": 0, "requester": "claude-code"}`), status: 400,
			text: "port must be between 1 and 65535, not 0"},
		{name: "passthrough requester", run: post("apollo", "/v1/passthroughs", `{"port": 3000, "requester": "Bad"}`), status: 400,
			text: `requester must be 1 to 32 lowercase letters, digits and -, such as claude-code, not "Bad"`},
		{name: "passthrough session", run: post("apollo", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "session": "a\nb"}`), status: 400,
			text: "session must be at most 128 printable characters"},
		{name: "passthrough label", run: post("apollo", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "label": "`+long+`"}`), status: 400,
			text: "label must be at most 64 printable characters on one line"},
		{name: "passthrough grants", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.grants.err = errors.New("the grants file is unreadable")
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "token": "good"}`)
		}, status: 500, text: "the grants file is unreadable"},
		{name: "passthrough token revoked", run: post("apollo", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "token": "bad"}`), status: 401,
			text: "the token for claude-code on apollo was revoked or never existed; drop it and ask again without it"},
		{name: "passthroughs open", review: true, run: func(t *testing.T, e *errEnv) *httptest.ResponseRecorder {
			for i := range maxPassthroughs {
				e.openPass(t, 4000+i)
			}
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "session": "s", "token": "good"}`)
		}, status: 429, text: "16 passthroughs are open for this server; close one first"},
		{name: "passthrough refused", review: true, run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.s.mu.Lock()
			e.s.refuseLocked("apollo", kindPassthrough)
			e.s.mu.Unlock()
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`)
		}, status: 429, text: "refused; ask again in 30 s", retry: "30"},
		{name: "passthrough without reviewer", run: post("apollo", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`), status: 409,
			text: noReviewerText},
		{name: "passthroughs waiting", review: true, run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			for i := range 3 {
				e.do("apollo", "POST", "/v1/passthroughs", fmt.Sprintf(`{"port": %d, "requester": "claude-code"}`, 3001+i))
			}
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code"}`)
		}, status: 429, text: "3 passthrough requests are already waiting for approval; wait for them first"},
		{name: "passthrough not connected", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.passWithToken()
			e.pass.err = fmt.Errorf("open: %w", ErrNotConnected)
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "token": "good"}`)
		}, status: 409, text: "Nearhost isn't connected to apollo. On dev@laptop, select apollo in Nearhost and press c, then ask again"},
		{name: "passthrough open failed", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.passWithToken()
			e.pass.err = errors.New("ssh: the forward was refused")
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "token": "good"}`)
		}, status: 502, text: "ssh: the forward was refused"},
		{name: "passthrough closed while opening", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.passWithToken()
			e.closeSessionOnOpen()
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "session": "s", "token": "good"}`)
		}, status: 409, text: "its session was closed while port 3000 opened"},
		{name: "passthrough closed while opening, close failed", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.passWithToken()
			e.closeSessionOnOpen()
			e.failCloses()
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "session": "s", "token": "good"}`)
		}, status: 502, text: "its session was closed while port 3000 opened, and closing it failed: link down"},
		{name: "passthrough port held", review: true, run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "claude-code", "session": "s1"}`)
			return e.do("apollo", "POST", "/v1/passthroughs", `{"port": 3000, "requester": "codex", "session": "s2"}`)
		}, status: 409, text: "port 3000 on apollo is already waiting for approval for claude-code (session s1); ask for another port, or close that one first"},
		{name: "no such passthrough", run: get("apollo", "/v1/passthroughs/abc"), status: 404, text: "no such passthrough; it may have ended over 10 minutes ago"},
		{name: "no such passthrough to close", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			return e.do("apollo", "DELETE", "/v1/passthroughs/abc", "")
		}, status: 404, text: "no such passthrough; it may have ended over 10 minutes ago"},
		{name: "passthrough close failed", run: func(t *testing.T, e *errEnv) *httptest.ResponseRecorder {
			id := e.openPass(t, 3000)
			e.failCloses()
			return e.do("apollo", "DELETE", "/v1/passthroughs/"+id, "")
		}, status: 502, text: "link down"},
		{name: "session close failed", run: func(t *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.openPass(t, 3000)
			e.failCloses()
			return e.do("apollo", "DELETE", "/v1/passthroughs?session=s", "")
		}, status: 502, text: "link down"},
		{name: "say which session", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			return e.do("apollo", "DELETE", "/v1/passthroughs", "")
		}, status: 400, text: "say which session: DELETE /v1/passthroughs?session=<id>, or close one with DELETE /v1/passthroughs/<id>"},

		// documents_handlers.go
		{name: "document path with NUL", run: post("apollo", "/v1/documents", `{"path": "/srv/app/a\u0000b", "root": "/srv/app", "requester": "claude-code"}`),
			status: 400, text: "path must not contain NUL"},
		{name: "document path too long", run: post("apollo", "/v1/documents", `{"path": "/`+strings.Repeat("x", 1100)+`", "root": "/srv/app", "requester": "claude-code"}`),
			status: 400, text: "path must be at most 1024 bytes"},
		{name: "document network path", run: post("apollo", "/v1/documents", `{"path": "\\\\server\\share\\a.md", "root": "/srv/app", "requester": "claude-code"}`),
			status: 400, text: `path is a network path: network paths (\\server\share) aren't supported yet`},
		{name: "document path relative", run: post("apollo", "/v1/documents", `{"path": "a.md", "root": "/srv/app", "requester": "claude-code"}`),
			status: 400, text: `path must be absolute, not "a.md"`},
		{name: "document root relative", run: post("apollo", "/v1/documents", `{"path": "/srv/app/a.md", "root": "app", "requester": "claude-code"}`),
			status: 400, text: `root must be absolute, not "app"`},
		{name: "document requester", run: post("apollo", "/v1/documents", docBody("a.md", "Bad", "")), status: 400,
			text: `requester must be 1 to 32 lowercase letters, digits and -, such as claude-code, not "Bad"`},
		{name: "document session", run: post("apollo", "/v1/documents", doc(`, "session": "a\nb"`)), status: 400,
			text: "session must be at most 128 printable characters"},
		{name: "document label", run: post("apollo", "/v1/documents", doc(`, "label": "`+long+`"`)), status: 400,
			text: "label must be at most 64 printable characters on one line"},
		{name: "document not connected", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.sh.resolveErr = fmt.Errorf("resolve: %w", ErrNotConnected)
			return e.do("apollo", "POST", "/v1/documents", doc(""))
		}, status: 409, text: "Nearhost isn't connected to apollo. On dev@laptop, select apollo in Nearhost and press c, then ask again"},
		{name: "document not there", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.sh.resolveErr = fmt.Errorf("stat: %w", fs.ErrNotExist)
			return e.do("apollo", "POST", "/v1/documents", doc(""))
		}, status: 404, text: "/srv/app/a.md isn't there on apollo"},
		{name: "not a document", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.sh.resolveErr = fmt.Errorf("%w: a.md is a folder", ErrBadDocument)
			return e.do("apollo", "POST", "/v1/documents", doc(""))
		}, status: 400, text: "not a document nearhost can show: a.md is a folder"},
		{name: "document resolve failed", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.sh.resolveErr = errors.New("sftp: broken pipe")
			return e.do("apollo", "POST", "/v1/documents", doc(""))
		}, status: 502, text: "sftp: broken pipe"},
		{name: "document grants", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.dg.err = errors.New("the grants file is unreadable")
			return e.do("apollo", "POST", "/v1/documents", doc(`, "token": "tok"`))
		}, status: 500, text: "the grants file is unreadable"},
		{name: "document token revoked", run: post("apollo", "/v1/documents", doc(`, "token": "bad"`)), status: 401,
			text: "the token for claude-code on apollo was revoked or never existed; drop it and ask again without it"},
		{name: "document refused", review: true, run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.s.mu.Lock()
			e.s.refuseLocked("apollo", kindDocument)
			e.s.mu.Unlock()
			return e.do("apollo", "POST", "/v1/documents", doc(""))
		}, status: 429, text: "refused; ask again in 30 s", retry: "30"},
		{name: "document without reviewer", run: post("apollo", "/v1/documents", doc("")), status: 409, text: noReviewerText},
		{name: "documents waiting", review: true, run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			for _, d := range []string{"b.md", "c.md", "d.md"} {
				e.do("apollo", "POST", "/v1/documents", docBody(d, "claude-code", ""))
			}
			return e.do("apollo", "POST", "/v1/documents", doc(""))
		}, status: 429, text: "3 document requests are already waiting for approval; wait for them first"},
		{name: "document without page", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.dg.grant("apollo", "claude-code", "tok", "/srv/app")
			e.sh.showErr = fmt.Errorf("%w: --web-port is 0", ErrNoPage)
			return e.do("apollo", "POST", "/v1/documents", doc(`, "token": "tok"`))
		}, status: 409, text: "nearhost's page isn't served: --web-port is 0"},
		{name: "document show not connected", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.dg.grant("apollo", "claude-code", "tok", "/srv/app")
			e.sh.showErr = ErrNotConnected
			return e.do("apollo", "POST", "/v1/documents", doc(`, "token": "tok"`))
		}, status: 409, text: "Nearhost isn't connected to apollo. On dev@laptop, select apollo in Nearhost and press c, then ask again"},
		{name: "document show failed", run: func(_ *testing.T, e *errEnv) *httptest.ResponseRecorder {
			e.dg.grant("apollo", "claude-code", "tok", "/srv/app")
			e.sh.showErr = errors.New("the tunnel dropped")
			return e.do("apollo", "POST", "/v1/documents", doc(`, "token": "tok"`))
		}, status: 502, text: "the tunnel dropped"},
		{name: "no such document request", run: get("apollo", "/v1/documents/abc"), status: 404,
			text: "no such document request; it may have ended over 10 minutes ago"},
	}
	// Only a Windows laptop takes a drive path, which a server's request
	// may not send.
	if runtime.GOOS == "windows" {
		cases = append(cases, errCase{name: "document drive path from a server",
			run:    post("apollo", "/v1/documents", `{"path": "C:\\app\\a.md", "root": "C:\\app", "requester": "claude-code"}`),
			status: 400, text: `path must be a slash path such as /srv/app on apollo, not "C:\\app\\a.md"`})
	}
	return cases
}

// Every error keeps the status and the `error` text it has always had, as
// the skills already on servers read them, and is problem details now.
func TestEveryErrorKeepsStatusAndText(t *testing.T) {
	for _, c := range errorCases() {
		t.Run(c.name, func(t *testing.T) {
			e := newErrEnv(t, c.review)
			w := c.run(t, e)
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("not JSON: %q", w.Body)
			}
			if w.Code != c.status || out["error"] != c.text {
				t.Fatalf("got %d %q, want %d %q\n%s", w.Code, out["error"], c.status, c.text, w.Body)
			}
			if got := w.Header().Get("Retry-After"); got != c.retry {
				t.Fatalf("Retry-After %q, want %q", got, c.retry)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type %q", ct)
			}
			code, _ := out["code"].(string)
			entry, ok := report.Lookup(report.Code(code))
			if !ok || out["title"] != c.text || out["status"] != float64(c.status) || out["level"] != "error" {
				t.Fatalf("not problem details with a catalogued code: %s", w.Body)
			}
			if strings.HasPrefix(code, "agent.") && entry.Status != c.status && !strings.Contains(c.name, "close failed") {
				t.Fatalf("%s is catalogued as %d, answered with %d", code, entry.Status, c.status)
			}
		})
	}
}

// The skill takes the first "error" in a body as the error, so it comes
// first.
func TestErrorKeyComesFirst(t *testing.T) {
	for _, c := range errorCases() {
		t.Run(c.name, func(t *testing.T) {
			w := c.run(t, newErrEnv(t, c.review))
			if !bytes.HasPrefix(w.Body.Bytes(), []byte(`{"error":"`)) {
				t.Fatalf("%s", w.Body)
			}
		})
	}
}

// A reload that waits for a reviewer there is none of still lists what
// waits, after the error.
func TestReloadWithoutReviewerKeepsItsItems(t *testing.T) {
	e := newErrEnv(t, false)
	e.rel.answer(Reloaded{Items: []string{"web 8080", "host api.test"}, SHA256: "abc"}, nil)
	w := e.do("mini", "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	var out struct {
		Error string   `json:"error"`
		Items []string `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 409 || out.Error != noReviewerText || len(out.Items) != 2 || out.Items[1] != "host api.test" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// warningDoctor reports a warning as it checks, then answers with err.
type warningDoctor struct {
	warn *report.Message
	err  error
}

func (d warningDoctor) Doctor(ctx context.Context, _, _, _ string) ([]doctor.Finding, error) {
	report.To(ctx, d.warn)
	return nil, d.err
}

// Warnings reported while a request is handled come back with its reply,
// last, and on its error too; data under a name the skill reads is left
// out, so the skill never reads it as the reply's own.
func TestRepliesCarryTheirWarnings(t *testing.T) {
	warn := report.Warnf(report.DiscoveryIncomplete, "a preset could not be read").With("state", "applied").With("error", "x").With("path", "/p")
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{{"reply", nil, 200}, {"error", errors.New("ssh failed"), 502}} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Options{Laptop: "dev@laptop", Source: fakeSource{"mini": mini}, Doctor: warningDoctor{warn, tc.err}})
			t.Cleanup(s.Close)
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, request("mini", "GET", "/v1/doctor"+query("path", "/home/dev/code/myapp"), ""))
			body := w.Body.String()
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, body)
			}
			i := strings.Index(body, `"warnings":[`)
			if i < 0 || !strings.HasSuffix(strings.TrimSpace(body), "]}") || strings.Contains(body[i:], `"state"`) || strings.Contains(body[i:], `"error"`) {
				t.Fatalf("warnings are not last, or carry a name the skill reads: %s", body)
			}
			var out struct {
				Warnings []*report.Message `json:"warnings"`
			}
			json.Unmarshal(w.Body.Bytes(), &out)
			if len(out.Warnings) != 1 || out.Warnings[0].Title != "a preset could not be read" || out.Warnings[0].Data["path"] != "/p" {
				t.Fatalf("%s", body)
			}
			if warn.Data["state"] != "applied" {
				t.Fatal("the warning reported was changed")
			}
		})
	}
	// Without warnings there is no key.
	s := New(Options{Laptop: "dev@laptop", Source: fakeSource{"mini": mini}})
	t.Cleanup(s.Close)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, request("mini", "GET", "/v1/projects", ""))
	if strings.Contains(w.Body.String(), "warnings") {
		t.Fatalf("%s", w.Body)
	}
}

// A project's problem is a message with no `error` key of its own and no
// data under a name the skill reads; the project's error text stays.
func TestProjectProblemIsSafeForTheSkill(t *testing.T) {
	m := report.Errorf(report.PortInUse, "port 8080 is in use").Because("Another program listens on it.").
		Quote(`"state":"running"`).With("state", "running").With("port", 8080)
	m.Context = "start web"
	p := Project{Name: "web", Path: "/w", State: "error", Error: "start web: port 8080 is in use", Problem: m}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, `"problem":`)
	if i < 0 || strings.Count(s, `"error":`) != 1 || strings.Count(s, `"state":`) != 1 || !strings.Contains(s[i:], `"port":8080`) ||
		!strings.Contains(s[i:], `"context":"start web"`) {
		t.Fatalf("%s", s)
	}
	if m.Data["state"] != "running" {
		t.Fatal("the project's problem was changed")
	}
	var back Project
	if err := json.Unmarshal(b, &back); err != nil || back.Problem == nil || back.Problem.Code != report.PortInUse || back.Error != p.Error {
		t.Fatalf("%v %+v", err, back)
	}
}

// TestMain keeps the warnings tests report out of the test output: the
// core sends what is reported without a sink to its log.
func TestMain(m *testing.M) {
	report.Fallback = report.SinkFunc(func(*report.Message) {})
	os.Exit(m.Run())
}
