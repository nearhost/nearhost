package tui

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/proxy"
)

// goldenEntry is entry i of the log the golden tests draw: requests with
// several methods and statuses, some with URLs that truncate; log lines of
// the projects, short and long enough to wrap, some failing; and notes with
// wide characters.
func goldenEntry(i int, at time.Time) logEntry {
	when := at.Add(time.Duration(i) * time.Second)
	long := "/assets/" + strings.Repeat("segment-", 1+i%30) + fmt.Sprintf("file-%d.js?v=%d&token=%s", i, i*7, strings.Repeat("x", i%90))
	switch i % 9 {
	case 0, 1, 2:
		method := []string{"GET", "POST", "PUT", "DELETE"}[i%4]
		status := []int{200, 201, 301, 404, 500, 101}[i%6]
		uri := fmt.Sprintf("/api/projects?page=%d", i)
		if i%3 == 1 {
			uri = long
		}
		id := ""
		if i%27 == 0 {
			id = goldenKept
		}
		return logEntry{at: when, source: []string{"myapp", "website", "blog"}[i%3], err: status >= 500,
			req: &proxy.Access{Method: method, Host: "myapp.test", URI: uri, Status: status, Took: time.Duration(i%1500) * time.Millisecond,
				Size: i * 13, ContentType: "application/json", ID: id, Headers: map[string][]string{"Accept": {"*/*"}, "Cookie": {"REDACTED"}}}}
	case 3:
		return logEntry{at: when, source: "myapp", text: clean(fmt.Sprintf("vite v5.%d ready in %d ms", i%9, i%700))}
	case 4:
		return logEntry{at: when, source: "myapp", err: true,
			text: clean(fmt.Sprintf("Error: cannot read file /srv/app/%s/index.ts: ENOENT no such file or directory, open '/srv/app/%s'", strings.Repeat("deep/", 1+i%12), strings.Repeat("deep-", 1+i%12)))}
	case 5:
		return logEntry{at: when, source: "proxy", text: clean(strings.Repeat("a long note that wraps over several lines ", 1+i%11) + fmt.Sprint(i))}
	case 6:
		return logEntry{at: when, source: "nearhost", text: clean("Opened 日本語のドキュメント と 文書 for the agent 😀 " + strings.Repeat("終わり ", i%20))}
	case 7:
		return logEntry{at: when, source: "myapp", text: clean("line one\nline two\ttabbed\x1b[31m red\x1b[0m " + strings.Repeat("and then more words ", i%6))}
	}
	return logEntry{at: when, source: "myapp", text: ""}
}

// goldenKept is the ID of the one exchange the fake backend keeps: a POST
// with a JSON body, a cookie and an authorization header, and a long answer.
const goldenKept = "k1"

// goldenAt is when the golden log starts.
var goldenAt = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// addGolden files n entries, numbered from first, in the combined log and
// the log of myapp.
func addGolden(m *Model, first, n int) {
	for i := first; i < first+n; i++ {
		m.logs.add(goldenEntry(i, goldenAt), rowKey(rowProject, "myapp"))
	}
}

// goldenModel is a model of width×height whose log, n entries long, is open
// at key: the combined log (2000 entries at most), or myapp's, which keeps
// the last 1000.
func goldenModel(t testing.TB, width, height int, key string, n int) *Model {
	t.Helper()
	f := newFake(t, sampleConfig())
	f.edit.captured = map[string]proxy.Exchange{goldenKept: {
		ID: goldenKept, Method: "POST", Host: "myapp.test", URI: "/api/projects",
		ReqHeader:   http.Header{"Content-Type": {"application/json"}, "Cookie": {"session=secret"}, "Authorization": {"Bearer tok"}},
		ReqBody:     []byte(`{"name":"demo","tags":["a","b"]}`),
		ReqBodySize: 31, Status: 201,
		RespHeader:   http.Header{"Content-Type": {"application/json"}},
		RespBody:     []byte(strings.Repeat(`{"line":"a long line of the body that is cut to the width"}`+"\n", 14)),
		RespBodySize: 800,
	}}
	m := New(f, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	addGolden(m, 0, n)
	m.sel = key
	press(m, "l")
	if m.logView == nil {
		t.Fatal("the log did not open")
	}
	return m
}

// goldenRun draws the log with the live code and with the reference after
// every step, and fails when the frames differ by a byte.
type goldenRun struct {
	t    *testing.T
	m    *Model
	ref  viewport.Model
	next int // the next entry to add
}

// check compares one frame, and the pieces the footer and the tabs are made
// of.
func (g *goldenRun) check(label string) {
	g.t.Helper()
	m := g.m
	want := refLogScreen(m, &g.ref)
	got := m.logScreen()
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range max(len(gl), len(wl)) {
			var a, b string
			if i < len(gl) {
				a = gl[i]
			}
			if i < len(wl) {
				b = wl[i]
			}
			if a != b {
				g.t.Fatalf("%s: line %d differs:\n got %q\nwant %q", label, i, a, b)
			}
		}
		g.t.Fatalf("%s: frames differ", label)
	}
	if a, b := m.logTabs(), refLogTabs(m); a != b {
		g.t.Fatalf("%s: tabs %q, want %q", label, a, b)
	}
	shown, refShown := m.shown(), refShown(m)
	if len(shown) != len(refShown) {
		g.t.Fatalf("%s: shows %d entries, want %d", label, len(shown), len(refShown))
	}
	for i := range shown {
		if shown[i].seq != refShown[i].seq {
			g.t.Fatalf("%s: entry %d is %d, want %d", label, i, shown[i].seq, refShown[i].seq)
		}
	}
	if a, b := m.logCount(len(shown)), refLogCount(m, len(refShown)); a != b {
		g.t.Fatalf("%s: count %q, want %q", label, a, b)
	}
}

// keys presses each key and checks the frame after it.
func (g *goldenRun) keys(label string, keys ...string) {
	g.t.Helper()
	for i, k := range keys {
		press(g.m, k)
		g.check(fmt.Sprintf("%s, key %d %q", label, i, k))
	}
}

// repeat presses key n times, checking after each.
func (g *goldenRun) repeat(label, key string, n int) {
	g.t.Helper()
	for i := 0; i < n; i++ {
		press(g.m, key)
		g.check(fmt.Sprintf("%s, %s %d", label, key, i))
	}
}

// add files n more entries, which drops the oldest ones once the log is full,
// and checks the frame.
func (g *goldenRun) add(label string, n int) {
	g.t.Helper()
	addGolden(g.m, g.next, n)
	g.next += n
	g.check(label)
}

// openKept opens a request whose exchange the backend kept, nearest the end.
func (g *goldenRun) openKept(label string) {
	g.t.Helper()
	shown := g.m.shown()
	for i := len(shown) - 1; i >= 0; i-- {
		if shown[i].req != nil && shown[i].req.ID != "" {
			g.m.logView.open = shown[i].seq
			g.check(label)
			return
		}
	}
	g.t.Fatalf("%s: no kept request to open", label)
}

// openRequest opens the n-th request counting back from the newest shown.
func (g *goldenRun) openRequest(label string, fromEnd int) {
	g.t.Helper()
	shown := g.m.shown()
	for i := len(shown) - 1; i >= 0; i-- {
		if shown[i].req != nil {
			if fromEnd == 0 {
				g.m.logView.open = shown[i].seq
				g.check(label)
				return
			}
			fromEnd--
		}
	}
	g.t.Fatalf("%s: no request to open", label)
}

// Every state of a 2000-entry log draws the same bytes as it did before only
// the rows on screen were rendered.
func TestLogViewGolden(t *testing.T) {
	scenarios := []struct {
		name string
		full bool // needs a log that fills its ring; the others use 300 entries
		run  func(g *goldenRun)
	}{
		{"end while following", true, func(g *goldenRun) { g.check("start") }},
		{"top", true, func(g *goldenRun) { g.keys("top", "g") }},
		{"middle", true, func(g *goldenRun) { g.keys("top", "g"); g.repeat("middle", "pgdown", 8) }},
		{"one row from the end", true, func(g *goldenRun) { g.keys("up", "k", "j", "k", "k") }},
		{"line by line from the top", false, func(g *goldenRun) { g.keys("top", "g"); g.repeat("down", "j", 24); g.repeat("up", "k", 12) }},
		{"home and end", false, func(g *goldenRun) { g.keys("ends", "g", "G", "home", "end", "pgup", "pgup", "pgdown") }},
		{"wheel", false, func(g *goldenRun) {
			for i := 0; i < 8; i++ {
				g.m.logWheel(tea.Mouse{Button: tea.MouseWheelUp})
				g.check(fmt.Sprintf("wheel up %d", i))
			}
			g.m.logWheel(tea.Mouse{Button: tea.MouseWheelDown})
			g.check("wheel down")
		}},
		{"search", false, func(g *goldenRun) {
			g.keys("open search", "/")
			for _, r := range "page=1" {
				g.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
				g.check("typing " + string(r))
			}
			g.keys("kept", "enter", "k", "k", "g", "j", "pgdown", "G")
			g.keys("cleared", "esc")
		}},
		{"search that matches nothing", false, func(g *goldenRun) {
			g.keys("open search", "/")
			typeText(g.m, "no such text anywhere")
			g.check("typed")
			g.keys("kept", "enter", "g", "G")
		}},
		{"search in the text of notes", false, func(g *goldenRun) {
			g.keys("open search", "/")
			typeText(g.m, "ENOENT")
			g.keys("kept", "enter", "k", "k", "k", "g", "pgdown")
		}},
		{"each kind", false, func(g *goldenRun) {
			for _, k := range []string{"tab", "tab", "tab", "tab"} {
				g.keys("kind", k)
				g.keys("moved in kind", "k", "k", "pgup", "g", "pgdown", "G")
			}
			g.keys("back", "shift+tab", "shift+tab")
		}},
		{"kind and search", false, func(g *goldenRun) {
			g.keys("kind", "tab", "tab", "/")
			typeText(g.m, "proj")
			g.keys("kept", "enter", "k", "g", "j")
		}},
		{"expanded entry inside the window", false, func(g *goldenRun) {
			g.openRequest("open the last request", 0)
			g.keys("scroll", "k", "k", "g", "G")
			g.repeat("up through it", "k", 15)
		}},
		{"expanded entry above the window", false, func(g *goldenRun) {
			g.openRequest("open a request far up", 60)
			g.keys("top", "g")
			g.repeat("down", "pgdown", 10)
			g.keys("end", "G", "k")
		}},
		{"expanded entry below the window", false, func(g *goldenRun) {
			g.keys("top", "g")
			g.openRequest("open the newest request", 0)
			g.keys("move", "j", "pgdown", "pgdown")
		}},
		{"expanded with the cursor on it", false, func(g *goldenRun) {
			g.keys("up", "g")
			g.repeat("down", "j", 12)
			g.keys("expand", "enter", "j", "j", "enter", "k", "enter", "enter")
		}},
		{"expanded entry and a search", false, func(g *goldenRun) {
			g.openRequest("open", 3)
			g.keys("search", "/")
			typeText(g.m, "GET")
			g.keys("kept", "enter", "k", "g", "G")
		}},
		{"no wrap", false, func(g *goldenRun) {
			g.keys("unwrap", "w", "g", "pgdown", "G")
			g.openRequest("open", 2)
			g.keys("wrap again", "w", "k")
		}},
		{"reveal", false, func(g *goldenRun) {
			g.openKept("open a kept request")
			v := g.m.logView
			v.follow, v.cursor = false, v.open
			g.check("cursor on it")
			if s := screen(g.m); !strings.Contains(s, "cookie: •••") || strings.Contains(s, "session=secret") {
				g.t.Fatalf("the secret headers are not hidden:\n%s", s)
			}
			g.keys("reveal", "v")
			if s := screen(g.m); !strings.Contains(s, "cookie: session=secret") {
				g.t.Fatalf("the secret headers are not revealed:\n%s", s)
			}
			g.keys("hide again, move", "v", "k", "v", "j", "w")
		}},
		{"help", false, func(g *goldenRun) { g.keys("help", "?", "k", "?", "g", "?", "esc") }},
		{"toast and confirm", false, func(g *goldenRun) {
			g.m.logView.said = "Sent GET /x again: 200."
			g.check("said")
			g.keys("up", "k")
			g.keys("down", "g")
			shown := g.m.shown()
			g.m.logView.confirm = shown[len(shown)-1].seq
			g.check("confirm")
		}},
		{"new lines while following", false, func(g *goldenRun) { g.add("one", 1); g.add("many", 40); g.add("a page", 300) }},
		{"new lines while paused", false, func(g *goldenRun) {
			g.keys("up", "k", "k")
			g.add("one", 1)
			g.add("many", 40)
			g.keys("down", "j")
			g.add("a page", 300)
			g.keys("follow", "G")
		}},
		{"entries dropped while scrolled up", true, func(g *goldenRun) {
			g.keys("up", "g")
			g.repeat("down", "pgdown", 5)
			for i := 0; i < 4; i++ {
				g.add(fmt.Sprintf("append and drop %d", i), 300)
			}
			g.keys("move", "k", "j", "pgup")
		}},
		{"the entry under the cursor is dropped", true, func(g *goldenRun) {
			g.keys("up", "g", "j", "j", "j")
			g.add("append past the cap", 2500)
			g.keys("after", "j", "k", "g")
		}},
		{"entries dropped with a request open", true, func(g *goldenRun) {
			g.keys("up", "g")
			g.repeat("down", "pgdown", 3)
			g.openRequest("open", 150)
			for i := 0; i < 3; i++ {
				g.add(fmt.Sprintf("append %d", i), 400)
			}
			g.keys("end", "G")
		}},
		{"entries dropped under a search", true, func(g *goldenRun) {
			g.keys("search", "/")
			typeText(g.m, "page")
			g.keys("kept", "enter", "g")
			g.repeat("down", "pgdown", 3)
			for i := 0; i < 5; i++ {
				g.add(fmt.Sprintf("append %d", i), 400)
			}
		}},
	}
	for _, size := range [][2]int{{80, 24}, {140, 50}} {
		for _, key := range []string{homeKey, rowKey(rowProject, "myapp")} {
			for _, sc := range scenarios {
				// The scenarios on a short log run in the two corners, small
				// window on Home and big window on a project; those that
				// need a full ring run in all four.
				if !sc.full && (size[0] == 80) != (key == homeKey) {
					continue
				}
				t.Run(fmt.Sprintf("%dx%d/%s/%s", size[0], size[1], strings.TrimPrefix(key, "project:"), sc.name), func(t *testing.T) {
					n := 300
					if sc.full {
						n = map[string]int{homeKey: 2000}[key]
						if n == 0 {
							n = ownLogLines
						}
					}
					m := goldenModel(t, size[0], size[1], key, n)
					sc.run(&goldenRun{t: t, m: m, next: n, ref: viewport.New()})
				})
			}
		}
	}
}
