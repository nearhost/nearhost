package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
)

const (
	// ownLogLines is how many lines each project and server keeps.
	ownLogLines = 1000
	// allLogLines is how many lines the combined log on Home keeps.
	allLogLines = 2000
)

// ownSource is the source of the interface's own lines.
const ownSource = "nearhost"

// logEntry is one line of a log.
type logEntry struct {
	seq    int // numbers the entries in the order they came
	at     time.Time
	source string        // the project, the server, "proxy", or ownSource
	text   string        // cleaned; empty for a request
	req    *proxy.Access // the request the proxy handled; nil for any other line
	err    bool          // a failed request, an error line, or a project in error
}

// plain is the entry as text, for searching.
func (e logEntry) plain() string {
	if e.req != nil {
		return fmt.Sprintf("%s %s %s %s %d", e.at.Format("15:04:05"), e.source, e.req.Method, clean(e.req.URI), e.req.Status)
	}
	return e.at.Format("15:04:05") + " " + e.source + " " + e.text
}

// ring keeps the last max entries added to it.
type ring struct {
	entries []logEntry
	max     int
}

func (r *ring) add(e logEntry) {
	r.entries = append(r.entries, e)
	if len(r.entries) > r.max {
		r.entries = append(r.entries[:0], r.entries[len(r.entries)-r.max:]...)
	}
}

// logs holds the combined log and one log per project and server, keyed by
// row key (see rowKey).
type logs struct {
	all ring
	by  map[string]*ring
	seq int
	now func() time.Time
}

func newLogs(now func() time.Time) *logs {
	return &logs{all: ring{max: allLogLines}, by: map[string]*ring{}, now: now}
}

// add numbers e and files it in the combined log and the logs of keys.
func (l *logs) add(e logEntry, keys ...string) {
	l.seq++
	e.seq = l.seq
	l.all.add(e)
	for _, key := range keys {
		r := l.by[key]
		if r == nil {
			r = &ring{max: ownLogLines}
			l.by[key] = r
		}
		r.add(e)
	}
}

// note adds a line of the interface's own to the combined log.
func (l *logs) note(line string) {
	l.add(logEntry{at: l.now(), source: ownSource, text: clean(line)})
}

// noteOn adds a line of the interface's own to the combined log and to the
// log of the row with key.
func (l *logs) noteOn(key, line string) {
	l.add(logEntry{at: l.now(), source: ownSource, text: clean(line)}, key)
}

// project adds a line of the interface's own about a project to its log and
// the combined log.
func (l *logs) project(name string, at time.Time, msg string) {
	l.add(logEntry{at: at, source: clean(name), text: clean(msg), err: true}, rowKey(rowProject, name))
}

// event files an engine event. A project's log gets its own lines and those
// of its server; a server's log gets the lines of the server and of its
// projects; the combined log gets everything.
func (l *logs) event(ev engine.Event, cfg *config.Config) {
	e := logEntry{at: ev.Time, source: "proxy"}
	switch {
	case ev.Project != "":
		e.source = clean(ev.Project)
	case ev.Server != "":
		e.source = clean(ev.Server)
	}
	switch {
	case ev.State != "":
		e.text, e.err = clean(ev.Message), ev.State == engine.StateError
	case ev.Server != "" && ev.Project == "":
		e.text = clean(ev.Message)
	default:
		if a, ok := proxy.ParseAccess(ev.Message); ok {
			e.req, e.err = &a, a.Status >= 500
		} else {
			e.text, e.err = clean(proxy.PlainLog(ev.Message)), proxy.Level(ev.Message) == "error"
		}
	}
	var keys []string
	switch {
	case ev.Project != "":
		keys = append(keys, rowKey(rowProject, ev.Project))
		if ev.Server != "" {
			keys = append(keys, rowKey(rowServer, ev.Server))
		}
	case ev.Server != "":
		keys = append(keys, rowKey(rowServer, ev.Server))
		for _, p := range cfg.Projects {
			if p.Server == ev.Server {
				keys = append(keys, rowKey(rowProject, p.Name))
			}
		}
	}
	l.add(e, keys...)
}

// entries is the log shown for the row with key, keeping only the entries
// that contain filter, ignoring case. Home shows the combined log.
func (l *logs) entries(key, filter string) []logEntry {
	var src []logEntry
	if key == homeKey {
		src = l.all.entries
	} else if r := l.by[key]; r != nil {
		src = r.entries
	}
	if filter == "" {
		return src
	}
	want := strings.ToLower(filter)
	var out []logEntry
	for _, e := range src {
		if strings.Contains(strings.ToLower(e.plain()), want) {
			out = append(out, e)
		}
	}
	return out
}

// The columns of a drawn entry: the time, then the method or source, then
// the rest.
const (
	logTimeWidth   = 8
	logSourceWidth = 8
	logTextAt      = logTimeWidth + 2 + logSourceWidth + 2
)

// entryLines draws an entry width cells wide. A request takes one line;
// any other entry wraps, its later lines under its text. A request's
// column holds its method, or with sources, where it came from, as on
// Home, where entries come from every project.
func entryLines(e logEntry, width int, sources bool) []string {
	lead := styleDim.Render(e.at.Format("15:04:05")) + "  "
	room := width - logTextAt
	if room < 8 {
		return []string{fit(lead+e.plain(), width)}
	}
	if e.req != nil {
		if sources {
			text := e.req.Method + " " + requestText(*e.req, room-len(e.req.Method)-1)
			return []string{fit(lead+fit(e.source, logSourceWidth)+"  "+text, width)}
		}
		return []string{fit(lead+fit(e.req.Method, logSourceWidth)+"  "+requestText(*e.req, room), width)}
	}
	text := e.text
	if e.err {
		text = styleRed.Render(text)
	}
	var out []string
	for i, l := range wrap(text, room) {
		if i == 0 {
			out = append(out, fit(lead+fit(e.source, logSourceWidth)+"  "+l, width))
			continue
		}
		out = append(out, fit(strings.Repeat(" ", logTextAt)+l, width))
	}
	return out
}

// requestText is a request's path, status and time, room cells wide.
func requestText(a proxy.Access, room int) string {
	took := styleDim.Render(fmt.Sprintf("%5s", tookText(a.Took)))
	tail := "  " + statusText(a.Status) + "  " + took
	if room < 20 {
		tail = "  " + statusText(a.Status)
	}
	return fit(clean(a.URI), room-ansi.StringWidth(tail)) + tail
}

// statusText is a response's status in the colour of its class.
func statusText(status int) string {
	s := fmt.Sprintf("%3d", status)
	switch {
	case status >= 500:
		return styleRed.Render(s)
	case status >= 400:
		return styleAmber.Render(s)
	case status >= 300:
		return styleDim.Render(s)
	case status == 101:
		return styleBeacon.Render(s)
	}
	return s
}

// tookText is how long a request took, in a few cells.
func tookText(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
