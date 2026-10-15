package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// request is an access log line of the proxy.
func request(method, uri string, status int) string {
	return fmt.Sprintf(`{"level":"info","msg":"handled request","request":{"method":%q,"host":"myapp.test","uri":%q,`+
		`"headers":{"Accept":["application/json"],"Cookie":["REDACTED"]}},"duration":0.212,"size":412,"status":%d,`+
		`"resp_headers":{"Content-Type":["application/json"]}}`, method, uri, status)
}

// logModel is a 120×36 model with myapp's log full of requests, running on
// mini, and its full-screen log open.
func logModel(t *testing.T, requests int) (*Model, *fakeBackend) {
	t.Helper()
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusConnected
	f.setState("myapp", engine.ProjectStatus{State: engine.StateRunning, WebLocalPort: 52114})
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	at := time.Date(2026, 10, 15, 12, 4, 0, 0, time.UTC)
	m.Update(eventMsg(engine.Event{Time: at, Project: "myapp", Server: "mini", State: engine.StateRunning, Message: "state: running"}))
	for i := 0; i < requests; i++ {
		status := 200
		if i == 1 {
			status = 500
		}
		m.Update(eventMsg(engine.Event{Time: at.Add(time.Duration(i) * time.Second), Project: "myapp", Server: "mini",
			Message: request("GET", fmt.Sprintf("/api/projects?page=%d", i), status)}))
	}
	m.sel = rowKey(rowProject, "myapp")
	press(m, "l")
	return m, f
}

// l opens a row's log over the whole window, and esc goes back.
func TestLogViewOpensAndCloses(t *testing.T) {
	m, _ := logModel(t, 3)
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 36 {
		t.Fatalf("drew %d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], " ∩ nearhost › myapp › log ") || !strings.HasSuffix(lines[0], "● running · https://myapp.test ") {
		t.Errorf("header %q", lines[0])
	}
	wantScreen(t, m, " all   requests   events   errors 1 ", "4 lines · following", "12:04:02  GET       /api/projects?page=2",
		"j/k scroll · enter expand · tab kind · / search")
	press(m, "esc")
	if m.logView != nil {
		t.Fatal("esc left the log open")
	}
	m.sel = homeKey
	press(m, "l")
	wantScreen(t, m, " ∩ nearhost › log ", "myapp     GET /api/projects?page=2")
}

// tab shows one kind of entry at a time.
func TestLogViewKinds(t *testing.T) {
	m, _ := logModel(t, 3)
	press(m, "tab") // requests
	if s := screen(m); strings.Contains(s, "state: running") || !strings.Contains(s, "3 of 4 lines") {
		t.Errorf("requests:\n%s", s)
	}
	press(m, "tab") // events
	if s := screen(m); !strings.Contains(s, "state: running") || strings.Contains(s, "page=0") {
		t.Errorf("events:\n%s", s)
	}
	press(m, "tab") // errors
	if s := screen(m); !strings.Contains(s, "page=1") || strings.Contains(s, "page=2") {
		t.Errorf("errors:\n%s", s)
	}
}

// / searches the log; enter keeps the search and esc clears it, then goes
// back.
func TestLogViewSearch(t *testing.T) {
	m, _ := logModel(t, 5)
	press(m, "/")
	typeText(m, "page=3")
	press(m, "enter")
	if s := screen(m); !strings.Contains(s, "/ page=3") || !strings.Contains(s, "1 of 6 lines") || strings.Contains(s, "page=2") {
		t.Errorf("searched:\n%s", s)
	}
	press(m, "esc")
	if m.logView == nil || m.logView.search.Value() != "" {
		t.Fatal("esc did not clear the search first")
	}
	press(m, "esc")
	if m.logView != nil {
		t.Error("esc did not go back")
	}
}

// Moving up stops following; new lines are counted until G follows again.
func TestLogViewFollow(t *testing.T) {
	m, _ := logModel(t, 3)
	press(m, "k")
	wantScreen(t, m, "4 lines · paused")
	at := time.Date(2026, 10, 15, 12, 5, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		m.Update(eventMsg(engine.Event{Time: at, Project: "myapp", Server: "mini", Message: request("GET", "/new", 200)}))
	}
	wantScreen(t, m, "▼ 2 new lines below", "G jump to the end and follow")
	press(m, "G")
	if s := screen(m); strings.Contains(s, "new lines below") || !strings.Contains(s, "6 lines · following") {
		t.Errorf("after G:\n%s", s)
	}
}

// enter opens the request under the cursor; c copies it as curl.
func TestLogViewOpensARequest(t *testing.T) {
	m, _ := logModel(t, 3)
	press(m, "k") // page=1, the 500
	press(m, "enter")
	wantScreen(t, m, "│ request  host: myapp.test · accept: application/json · cookie: REDACTED",
		"│ response 500 · application/json · 412 B", "│ route    80 web → 127.0.0.1:52114 → mini:80 over ssh", "c copy as curl · enter collapse")
	if got := clipboardOf(press(m, "c")); got != "curl -i 'https://myapp.test/api/projects?page=1' -H 'Accept: application/json'" {
		t.Errorf("copied %q", got)
	}
	press(m, "enter")
	if s := screen(m); strings.Contains(s, "│ request") {
		t.Errorf("enter did not close the request:\n%s", s)
	}
}

// A long log has a scrollbar.
func TestLogViewScrollbar(t *testing.T) {
	m, _ := logModel(t, 80)
	if s := screen(m); !strings.Contains(s, "┃") {
		t.Errorf("no scrollbar:\n%s", s)
	}
	m2, _ := logModel(t, 3)
	if s := screen(m2); strings.Contains(s, "┃") {
		t.Errorf("a short log has a scrollbar:\n%s", s)
	}
}

func TestCurlFor(t *testing.T) {
	a := proxy.Access{Method: "POST", Host: "myapp.test", URI: "/it's", Headers: map[string][]string{
		"Host": {"myapp.test"}, "Content-Type": {"application/json"}, "Authorization": {"REDACTED"},
	}}
	want := `curl -i -X POST 'https://myapp.test/it'\''s' -H 'Content-Type: application/json'`
	if got := curlFor(a); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSizeText(t *testing.T) {
	for n, want := range map[int]string{0: "", 412: "412 B", 3100: "3.1 kB", 1_200_000: "1.2 MB"} {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d) = %q, want %q", n, got, want)
		}
	}
}

// The log fills a small window exactly.
func TestLogViewInASmallWindow(t *testing.T) {
	m, _ := logModel(t, 40)
	m.Update(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})
	press(m, "k")
	press(m, "enter")
	lines := strings.Split(screen(m), "\n")
	if len(lines) != minHeight {
		t.Fatalf("drew %d lines", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != minWidth {
			t.Errorf("line %d is %d cells wide: %q", i, w, l)
		}
	}
}

// The log of a row with nothing in it yet takes every key.
func TestEmptyLogView(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.sel = rowKey(rowServer, "mini")
	press(m, "l")
	for _, k := range []string{"j", "k", "enter", "c", "G", "g", "tab", "w", "space"} {
		press(m, k)
	}
	wantScreen(t, m, "nearhost › mini › log", "0 of 0 lines · following")
}
