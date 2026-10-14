package tui

import (
	"strings"
	"time"

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

// ring keeps the last max lines added to it.
type ring struct {
	lines []string
	max   int
}

func (r *ring) add(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = append(r.lines[:0], r.lines[len(r.lines)-r.max:]...)
	}
}

// logs holds the combined log and one log per project and server, keyed by
// row key (see rowKey).
type logs struct {
	all ring
	by  map[string]*ring
}

func newLogs() *logs {
	return &logs{all: ring{max: allLogLines}, by: map[string]*ring{}}
}

func (l *logs) addTo(key, line string) {
	r := l.by[key]
	if r == nil {
		r = &ring{max: ownLogLines}
		l.by[key] = r
	}
	r.add(line)
}

// note adds a line of the interface's own to the combined log.
func (l *logs) note(line string) {
	l.all.add(clean(line))
}

// project adds a line of the interface's own about a project to its log and
// the combined log.
func (l *logs) project(name string, at time.Time, msg string) {
	line := at.Format("15:04:05") + " [" + clean(name) + "] " + clean(msg)
	l.all.add(line)
	l.addTo(rowKey(rowProject, name), line)
}

// event files an engine event. A project's log gets its own lines and those
// of its server; a server's log gets the lines of the server and of its
// projects; the combined log gets everything.
func (l *logs) event(ev engine.Event, cfg *config.Config) {
	msg := clean(ev.Message)
	source := "proxy"
	switch {
	case ev.Project != "":
		source = ev.Project
		if ev.State == "" {
			msg = clean(proxy.PlainLog(ev.Message))
		}
	case ev.Server != "":
		source = ev.Server
	default:
		msg = clean(proxy.PlainLog(ev.Message))
	}
	line := ev.Time.Format("15:04:05") + " [" + clean(source) + "] " + msg
	l.all.add(line)
	switch {
	case ev.Project != "":
		l.addTo(rowKey(rowProject, ev.Project), line)
		if ev.Server != "" {
			l.addTo(rowKey(rowServer, ev.Server), line)
		}
	case ev.Server != "":
		l.addTo(rowKey(rowServer, ev.Server), line)
		for _, p := range cfg.Projects {
			if p.Server == ev.Server {
				l.addTo(rowKey(rowProject, p.Name), line)
			}
		}
	}
}

// lines is the log shown for the row with key, keeping only the lines that
// contain filter, ignoring case. Home, and any key with no log yet, shows
// the combined log only for Home.
func (l *logs) lines(key, filter string) []string {
	var src []string
	if key == homeKey {
		src = l.all.lines
	} else if r := l.by[key]; r != nil {
		src = r.lines
	}
	if filter == "" {
		return src
	}
	want := strings.ToLower(filter)
	var out []string
	for _, line := range src {
		if strings.Contains(strings.ToLower(line), want) {
			out = append(out, line)
		}
	}
	return out
}
