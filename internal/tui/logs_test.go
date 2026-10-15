package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
)

var at = time.Date(2026, 10, 15, 12, 4, 9, 0, time.UTC)

func plainLines(lines []string) string {
	return ansi.Strip(strings.Join(lines, "\n"))
}

// A request is drawn in columns: time, method, path, status and how long it
// took.
func TestRequestEntry(t *testing.T) {
	e := logEntry{at: at, source: "myapp", req: &proxy.Access{Method: "GET", URI: "/dashboard", Status: 200, Took: 88 * time.Millisecond}}
	want := "12:04:09  GET       /dashboard                200   88ms"
	if got := plainLines(entryLines(e, len(want), false)); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	want = "12:04:09  myapp     GET /dashboard            200   88ms"
	if got := plainLines(entryLines(e, len(want), true)); got != want {
		t.Errorf("with sources got  %q\nwant %q", got, want)
	}
}

// Any other entry wraps under its text.
func TestEventEntryWraps(t *testing.T) {
	e := logEntry{at: at, source: "mini", text: "connected over ssh, 2 forwards reopened"}
	got := plainLines(entryLines(e, 40, false))
	want := "12:04:09  mini      connected over ssh, \n                    2 forwards reopened "
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Events are filed as requests, errors or plain lines.
func TestEventsAreSorted(t *testing.T) {
	l := newLogs(func() time.Time { return at })
	l.event(engine.Event{Time: at, Project: "myapp", Message: `{"level":"error","msg":"handled request","request":{"method":"GET","uri":"/x"},"duration":0.2,"status":502}`}, sampleConfig())
	l.event(engine.Event{Time: at, Project: "myapp", Message: `{"level":"error","msg":"dial tcp: refused"}`}, sampleConfig())
	l.event(engine.Event{Time: at, Project: "myapp", State: engine.StateError, Message: "state: error: boom"}, sampleConfig())
	l.event(engine.Event{Time: at, Project: "myapp", State: engine.StateRunning, Message: "state: running"}, sampleConfig())
	got := l.entries(rowKey(rowProject, "myapp"), "")
	if len(got) != 4 || got[0].req == nil || got[0].req.Status != 502 {
		t.Fatalf("entries %+v", got)
	}
	for i, wantErr := range []bool{true, true, true, false} {
		if got[i].err != wantErr {
			t.Errorf("entry %d err = %v", i, got[i].err)
		}
		if got[i].seq != i+1 {
			t.Errorf("entry %d seq = %d", i, got[i].seq)
		}
	}
	if got[1].text != "error: dial tcp: refused" {
		t.Errorf("error line %q", got[1].text)
	}
}

// A request's path from outside is cleaned and cut to its line.
func TestRequestEntryWithAnOddPath(t *testing.T) {
	e := logEntry{at: at, req: &proxy.Access{Method: "GET", URI: "/a\x1b[2J" + strings.Repeat("b", 200), Status: 200}}
	lines := entryLines(e, 60, false)
	if len(lines) != 1 || ansi.StringWidth(lines[0]) != 60 || strings.Contains(lines[0], "\x1b[2J") {
		t.Errorf("lines %q", lines)
	}
}
