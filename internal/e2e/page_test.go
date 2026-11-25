package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/testutil"
)

// These journeys answer coding agents on Nearhost's page, with no interface
// open: the core runs in the background, as `nearhost -d` starts it, with the
// page served on a port of the sandbox's own. An agent asks over this
// machine's agent socket, as the skill's script does, and the journey answers
// on the page as a browser on this machine would, through the real proxy.
//
// The page's key is a secret. The journeys get the keyed address from the
// core, as the interface does, and never print it, nor a cookie, nor an error
// that holds them.

// servePage has the core serve Nearhost's page on a port of the sandbox's
// own, in place of none.
func (s *sandbox) servePage() {
	s.t.Helper()
	s.web = testutil.ProxyPort(s.t)
	for i, a := range s.args {
		if a == "--web-port" {
			s.args[i+1] = fmt.Sprint(s.web)
		}
	}
}

// pageBase is the address of the sandbox's page, as the core gives it.
func (s *sandbox) pageBase() string {
	return fmt.Sprintf("https://%s:%d", hostname.PageHost, s.web)
}

// browser is a client for the sandbox's page that has no cookie yet, as a
// browser that never opened the page.
func (s *sandbox) browser() *http.Client {
	s.t.Helper()
	return browserClient(s.t, s.rootCert(), s.web)
}

// signedIn is a client for the sandbox's page that has opened the keyed
// address the core gives, as `nearhost status` prints it in a terminal, and
// so holds the page's cookie.
func (s *sandbox) signedIn() *http.Client {
	s.t.Helper()
	c, err := control.Dial(filepath.Join(s.dir, "nearhost", "run"))
	if err != nil {
		s.t.Fatalf("reach the core: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	keyed, err := c.PageKeyURL(ctx)
	if err != nil {
		s.t.Fatalf("the core gives no keyed address for its page: %v", err)
	}
	if !strings.HasPrefix(keyed, s.pageBase()+"/?") {
		// Only the part before the key is shown.
		s.t.Fatalf("the keyed address is not at %s", s.pageBase())
	}
	browser := s.browser()
	res := s.do(browser, http.MethodGet, keyed, nil, nil)
	if res.status != http.StatusOK || res.url != s.pageBase()+"/" {
		// The address it ended at may still hold the key: its path alone
		// is shown.
		at, _ := url.Parse(res.url)
		s.t.Fatalf("the keyed address answered %d at %s, want 200 at /", res.status, at.Path)
	}
	return browser
}

// pageReply is what the page answered: the status, the address it ended at
// after any redirect, and the body.
type pageReply struct {
	status int
	url    string
	body   string
}

// do asks the page with method for target, which may be a full address or a
// path on the page, sending form as the body when it is not nil and header
// besides. An error that stops the request fails the test without the
// address, which may hold the page's key.
func (s *sandbox) do(c *http.Client, method, target string, form url.Values, header http.Header) pageReply {
	s.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = s.pageBase() + target
	}
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		s.t.Fatalf("a request to the page: %v", withoutURL(err))
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := c.Do(req)
	if err != nil {
		s.t.Fatalf("%s to the page: %v", method, withoutURL(err))
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		s.t.Fatalf("read the page's answer: %v", withoutURL(err))
	}
	return pageReply{status: res.StatusCode, url: res.Request.URL.String(), body: string(b)}
}

// withoutURL is err without the address a *url.Error names.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// agentSocket is the path of the agent socket the sandbox's core serves on,
// in ~/.nearhost, once it is there.
func (s *sandbox) agentSocket() string {
	s.t.Helper()
	var found string
	testutil.WaitUntil(s.t, coreWait, func() bool {
		socks, _ := filepath.Glob(filepath.Join(s.dir, ".nearhost", "*.sock"))
		if len(socks) == 1 {
			found = socks[0]
		}
		return found != ""
	}, "the agent socket")
	return found
}

// agent is a coding agent on this machine, which talks to the core over its
// agent socket as the skill's script does.
type agent struct {
	t *testing.T
	c *http.Client
}

func (s *sandbox) agent() *agent {
	s.t.Helper()
	sock := s.agentSocket()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}
	s.t.Cleanup(tr.CloseIdleConnections)
	return &agent{t: s.t, c: &http.Client{Transport: tr}}
}

// call makes one call of the agent API and returns its status and the JSON
// object it answered.
func (a *agent) call(ctx context.Context, method, path string, body any) (int, map[string]any, error) {
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://nearhost"+path, in)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return res.StatusCode, nil, fmt.Errorf("the agent API answered %d with no JSON object: %w", res.StatusCode, err)
	}
	return res.StatusCode, out, nil
}

// docAsked is a document request waiting for an answer.
type docAsked struct {
	id, approveURL string
	// doc is the document's name, which the page and status show.
	doc string
}

// askToShow asks, as Claude Code, to show a document in a folder of its
// own, which waits for the user's answer.
func (a *agent) askToShow() docAsked {
	a.t.Helper()
	root := a.t.TempDir()
	const doc = "plan.md"
	if err := os.WriteFile(filepath.Join(root, doc), []byte("# The plan\n"), 0o600); err != nil {
		a.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	status, out, err := a.call(ctx, http.MethodPost, "/v1/documents", map[string]string{
		"path": filepath.Join(root, doc), "root": root,
		"requester": "claude-code", "session": "e2e",
	})
	if err != nil {
		a.t.Fatalf("ask to show a document: %v", err)
	}
	id, _ := out["id"].(string)
	link, _ := out["approve_url"].(string)
	if status != http.StatusAccepted || id == "" {
		a.t.Fatalf("asking to show a document answered %d %v, want 202 with an id", status, out)
	}
	return docAsked{id: id, approveURL: link, doc: doc}
}

// state is what the agent API says of the document request id now.
func (a *agent) state(id string) string {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	status, out, err := a.call(ctx, http.MethodGet, "/v1/documents/"+id, nil)
	if err != nil || status != http.StatusOK {
		a.t.Fatalf("read the document request: %d %v %v", status, out, err)
	}
	state, _ := out["state"].(string)
	return state
}

// wait starts the agent's wait for the document request id, as the skill's
// script waits, and returns where the state it ends in is sent.
func (a *agent) wait(id string) <-chan string {
	got := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), coreWait)
		defer cancel()
		status, out, err := a.call(ctx, http.MethodGet, "/v1/documents/"+id+"?wait=25", nil)
		if err != nil || status != http.StatusOK {
			got <- fmt.Sprintf("no state (%d %v %v)", status, out, err)
			return
		}
		state, _ := out["state"].(string)
		got <- state
	}()
	return got
}

// csrfField matches the form token in a form of the page.
var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// formToken is the form token of the approval view body.
func formToken(t *testing.T, body string) string {
	t.Helper()
	m := csrfField.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the approval view has no form token:\n%s", body)
	}
	return m[1]
}

// pageWithARequest starts the core in the background with the page served
// and the project "shop", and has an agent ask to show a document.
func pageWithARequest(t *testing.T) (*sandbox, *agent, docAsked) {
	t.Helper()
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.servePage()
	sb.startCore()
	sb.waitProject("shop", "running")
	ag := sb.agent()
	return sb, ag, ag.askToShow()
}

// An agent's document request, with no interface open, waits for an answer
// on the page: `nearhost status` lists it with the link the agent was given,
// the page lists it, and Yes there opens the document and ends the agent's
// wait.
func TestAnAgentIsAnsweredFromThePage(t *testing.T) {
	sb, ag, asked := pageWithARequest(t)
	if want := sb.pageBase() + "/approve/" + asked.id; asked.approveURL != want {
		t.Errorf("the agent was given %q to answer at, want %q", asked.approveURL, want)
	}

	// Printed to a pipe, status gives the page's address with no key.
	out, code := sb.nearhost("status")
	listed := "\nWaiting for your answer:\n  Claude Code on this machine asks to show " + asked.doc + ": " + asked.approveURL + "\n"
	if code != 0 || !strings.Contains(out, listed) || !strings.Contains(out, "\nPage: "+sb.pageBase()+"\n") {
		t.Errorf("nearhost status: exit %d, printed:\n%s\nwant the request listed as %q, and the page's address", code, out, listed)
	}
	if strings.Contains(out, "k=") {
		t.Errorf("nearhost status printed the page's key to a pipe")
	}

	browser := sb.signedIn()
	home := sb.do(browser, http.MethodGet, "/", nil, nil)
	if home.status != http.StatusOK || !strings.Contains(home.body, "Claude Code on this machine wants to show ") ||
		!strings.Contains(home.body, `href="/approve/`+asked.id+`"`) {
		t.Fatalf("the page answered %d without the request:\n%s", home.status, home.body)
	}
	view := sb.do(browser, http.MethodGet, asked.approveURL, nil, nil)
	if view.status != http.StatusOK || !strings.Contains(view.body, "Show a document") || !strings.Contains(view.body, asked.doc) {
		t.Fatalf("the approval view answered %d:\n%s", view.status, view.body)
	}

	waited := ag.wait(asked.id)
	answered := sb.do(browser, http.MethodPost, asked.approveURL,
		url.Values{"csrf": {formToken(t, view.body)}, "answer": {"yes"}},
		http.Header{"Origin": {sb.pageBase()}})
	if answered.status != http.StatusOK || answered.url != asked.approveURL+"?answered=yes" || !strings.Contains(answered.body, "Answered: Yes.") {
		t.Fatalf("Yes on the page answered %d at %s:\n%s", answered.status, answered.url, answered.body)
	}
	select {
	case state := <-waited:
		if state != "open" {
			t.Errorf("the agent's wait ended %s, want open", state)
		}
	case <-time.After(coreWait):
		t.Fatalf("the agent's wait did not end in %s", coreWait)
	}
	// The core opened the document, in the stand-in for the browser. What
	// it opened is not printed: it may hold the page's key.
	if opened, _ := os.ReadFile(sb.openedPath()); !strings.Contains(string(opened), asked.doc) {
		t.Errorf("the core opened no address with %s in it", asked.doc)
	}

	out, code = sb.nearhost("status")
	if code != 0 || strings.Contains(out, "Waiting for your answer:") || strings.Contains(out, asked.id) {
		t.Errorf("nearhost status after Yes: exit %d, printed:\n%s", code, out)
	}
	if home := sb.do(browser, http.MethodGet, "/", nil, nil); !strings.Contains(home.body, "Nothing waits for your answer.") {
		t.Errorf("the page after Yes:\n%s", home.body)
	}

	if out, code := sb.nearhost("stop"); code != 0 || out != "Stopped.\n" {
		t.Fatalf("nearhost stop: exit %d, printed %q", code, out)
	}
}

// A POST to an approval from another site, with the user's cookie and the
// right form token, is refused, and so is one from the page without the
// token: the request still waits.
func TestThePageRefusesAForeignPost(t *testing.T) {
	sb, ag, asked := pageWithARequest(t)
	browser := sb.signedIn()
	view := sb.do(browser, http.MethodGet, asked.approveURL, nil, nil)
	token := formToken(t, view.body)

	foreign := sb.do(browser, http.MethodPost, asked.approveURL,
		url.Values{"csrf": {token}, "answer": {"yes"}},
		http.Header{"Origin": {"https://app.test"}})
	if foreign.status != http.StatusForbidden {
		t.Errorf("a POST from https://app.test answered %d, want 403:\n%s", foreign.status, foreign.body)
	}
	tokenless := sb.do(browser, http.MethodPost, asked.approveURL,
		url.Values{"answer": {"yes"}},
		http.Header{"Origin": {sb.pageBase()}})
	if tokenless.status != http.StatusForbidden {
		t.Errorf("a POST without the form token answered %d, want 403:\n%s", tokenless.status, tokenless.body)
	}

	if state := ag.state(asked.id); state != "pending" {
		t.Errorf("the request is %s after the refused POSTs, want pending", state)
	}
	if opened, _ := os.ReadFile(sb.openedPath()); len(opened) != 0 {
		t.Errorf("the core opened an address after the refused POSTs")
	}
	if home := sb.do(browser, http.MethodGet, "/", nil, nil); !strings.Contains(home.body, `href="/approve/`+asked.id+`"`) {
		t.Errorf("the page no longer lists the request:\n%s", home.body)
	}
}

// The link an agent passes on, opened in a browser that never had the page's
// cookie, shows how to get it and nothing of the request, and approves
// nothing.
func TestTheLinkWithoutTheCookie(t *testing.T) {
	sb, ag, asked := pageWithARequest(t)
	got := sb.do(sb.browser(), http.MethodGet, asked.approveURL, nil, nil)
	if got.status != http.StatusForbidden || !strings.Contains(got.body, "Open Nearhost&#39;s page from this machine first") {
		t.Errorf("the link without the cookie answered %d:\n%s", got.status, got.body)
	}
	for _, detail := range []string{asked.id, asked.doc, "Claude Code", "/approve/", fmt.Sprint(sb.web), fmt.Sprint(sb.https)} {
		if strings.Contains(got.body, detail) {
			t.Errorf("the locked page shows %q:\n%s", detail, got.body)
		}
	}
	if state := ag.state(asked.id); state != "pending" {
		t.Errorf("the request is %s after the link was opened, want pending", state)
	}
}

// The page's event stream, through the proxy, says the page changed once an
// agent asks for something: it reaches the browser as it is sent, not when
// the stream ends.
func TestThePageRefreshesThroughTheProxy(t *testing.T) {
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.servePage()
	sb.startCore()
	sb.waitProject("shop", "running")
	ag := sb.agent()
	browser := sb.signedIn()

	ctx, cancel := context.WithTimeout(context.Background(), coreWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sb.pageBase()+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := browser.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", withoutURL(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("GET /events answered %d, %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	// The stream says it is connected first: from then on it follows the
	// core's changes.
	next := func(within time.Duration) (string, bool) {
		select {
		case l, ok := <-lines:
			return l, ok
		case <-time.After(within):
			return "", false
		}
	}
	if l, ok := next(stepWait); !ok || l != ": connected" {
		t.Fatalf("the event stream began with %q, want : connected", l)
	}

	asked := ag.askToShow()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, ok := next(time.Until(deadline))
		if !ok {
			t.Fatal("no data: changed through the proxy within 5s of the request")
		}
		if l == "data: changed" {
			break
		}
	}
	if home := sb.do(browser, http.MethodGet, "/", nil, nil); !strings.Contains(home.body, `href="/approve/`+asked.id+`"`) {
		t.Errorf("the page, refreshed, does not list the request:\n%s", home.body)
	}
}

// A document request shows in the interface's box and on the page alike.
// Answered on the page, the box closes, and the interface logs no error.
func TestTheTUIDropsWhatThePageAnswered(t *testing.T) {
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.servePage()
	term := sb.start()
	skipWelcome(term)
	term.waitFor("the project running", "● shop")

	ag := sb.agent()
	asked := ag.askToShow()
	term.waitFor("the request's box", "Show a document", "wants to show")

	browser := sb.signedIn()
	view := sb.do(browser, http.MethodGet, asked.approveURL, nil, nil)
	answered := sb.do(browser, http.MethodPost, asked.approveURL,
		url.Values{"csrf": {formToken(t, view.body)}, "answer": {"yes"}},
		http.Header{"Origin": {sb.pageBase()}})
	if answered.status != http.StatusOK || !strings.Contains(answered.body, "Answered: Yes.") {
		t.Fatalf("Yes on the page answered %d:\n%s", answered.status, answered.body)
	}
	term.waitGone("the box to close", "Show a document")
	if state := ag.state(asked.id); state != "open" {
		t.Errorf("the request is %s after Yes on the page, want open", state)
	}

	term.press('l')
	term.waitFor("the full-screen log", "esc back")
	term.waitFor("the request in the log", "wants to show plan.md")
	if s := term.screen(); strings.Contains(s, "Could not") || strings.Contains(s, "already ended") || strings.Contains(s, "failed") {
		t.Errorf("the interface logged an error:\n%s", s)
	}
	term.press(keyEscape)
	term.waitGone("the full-screen log to close", "esc back")
	quit(term)
}
