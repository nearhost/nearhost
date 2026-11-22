package page

import (
	"io"
	"net/http"
	"time"
)

// throttle is the least time between two changes the stream sends: those
// that come closer together are sent as one, at the end of the window.
const throttle = 250 * time.Millisecond

// keepalive is how often the stream sends a comment while nothing changes,
// so a connection that has gone is noticed. Tests shorten it.
var keepalive = 15 * time.Second

// events serves /events: a server-sent event stream that says data: changed
// when what the page shows has changed, at most once every throttle, until
// the request or the page ends. A HEAD gets the headers alone.
func (p *page) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	if r.Method == http.MethodHead {
		return
	}
	var changed <-chan struct{}
	if p.o.Watch != nil {
		var stop func()
		changed, stop = p.o.Watch()
		defer stop()
	}
	rc := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	send := func(s string) bool {
		if _, err := io.WriteString(w, s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(": connected\n\n") {
		return
	}
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	// due fires at the end of the window a change opened; nil while no
	// change waits to be sent.
	var due <-chan time.Time
	for {
		select {
		case <-r.Context().Done():
			return
		case <-p.o.Done:
			return
		case <-changed:
			if due == nil {
				due = time.After(throttle)
			}
		case <-due:
			due = nil
			if !send("data: changed\n\n") {
				return
			}
		case <-tick.C:
			if !send(": keepalive\n\n") {
				return
			}
		}
	}
}
