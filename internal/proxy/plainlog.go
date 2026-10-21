package proxy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// proxyLine is the part of a Caddy JSON log line that PlainLog and
// ParseAccess read.
type proxyLine struct {
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Error   string `json:"error"`
	Request *struct {
		Method  string              `json:"method"`
		Host    string              `json:"host"`
		URI     string              `json:"uri"`
		Headers map[string][]string `json:"headers"`
	} `json:"request"`
	Status      int                 `json:"status"`
	Duration    float64             `json:"duration"` // seconds
	Size        int                 `json:"size"`
	RespHeaders map[string][]string `json:"resp_headers"`
	ID          string              `json:"nearhost_id"`
}

// parse reads a JSON log line of the proxy; ok is false for any other line.
func parse(line string) (l proxyLine, ok bool) {
	if !strings.HasPrefix(line, "{") {
		return l, false
	}
	if err := json.Unmarshal([]byte(line), &l); err != nil || l.Msg == "" {
		return l, false
	}
	return l, true
}

// Access is a request the proxy handled, as its access log tells it.
type Access struct {
	Method, Host, URI string
	Status            int
	Took              time.Duration
	Size              int // of the response body, in bytes
	// Headers are the request's headers as logged. Caddy logs the values of
	// credentials, such as Cookie and Authorization, as REDACTED.
	Headers     map[string][]string
	ContentType string // the response's
	Location    string // the response's, for a redirect
	// ID names the exchange the proxy kept (see Captured); empty when it
	// keeps none.
	ID string
}

// ParseAccess reads a line of the proxy's access log; ok is false for any
// other line.
func ParseAccess(line string) (a Access, ok bool) {
	l, ok := parse(line)
	if !ok || l.Request == nil || l.Msg != "handled request" {
		return Access{}, false
	}
	first := func(h map[string][]string, name string) string {
		if v := h[name]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	return Access{
		Method: l.Request.Method, Host: l.Request.Host, URI: l.Request.URI,
		Status:      l.Status,
		Took:        time.Duration(l.Duration * float64(time.Second)).Round(time.Millisecond),
		Size:        l.Size,
		Headers:     l.Request.Headers,
		ContentType: first(l.RespHeaders, "Content-Type"),
		Location:    first(l.RespHeaders, "Location"),
		ID:          l.ID,
	}, true
}

// Level is the level of a JSON log line of the proxy, such as "error"; ""
// for any other line.
func Level(line string) string {
	l, _ := parse(line)
	return l.Level
}

// quietMessages are messages of the proxy that only report how Nearhost
// configures it, so showing them tells the user nothing to act on.
var quietMessages = []string{
	// Nearhost turns Caddy's admin endpoint off on purpose (see BuildConfig),
	// and Caddy warns about that on every load.
	"admin endpoint disabled",
}

// quiet reports whether line is a log line of the proxy whose message is
// one of quietMessages.
func quiet(line string) bool {
	l, ok := parse(line)
	return ok && slices.Contains(quietMessages, l.Msg)
}

// PlainLog turns a log line of the proxy, which writes JSON, into a short
// plain one: "GET /path 200 in 12ms" for a handled request, otherwise
// "<level>: <message>", followed by the request it is about when there is
// one. A routine message (see quietMessages) becomes "". Any other line is
// returned as it is.
func PlainLog(line string) string {
	if quiet(line) {
		return ""
	}
	l, ok := parse(line)
	if !ok {
		return line
	}
	// Only the access log gets the short form. An error line about a request
	// (an upstream that is down) carries a status too, and its message is
	// the reason.
	if a, ok := ParseAccess(line); ok {
		return fmt.Sprintf("%s %s %d in %s", a.Method, a.URI, a.Status, a.Took)
	}
	out := l.Msg
	if l.Level != "" {
		out = l.Level + ": " + out
	}
	if l.Error != "" {
		out += ": " + l.Error
	}
	if l.Request != nil {
		req := l.Request.Method + " " + l.Request.URI
		if l.Status != 0 {
			req += fmt.Sprintf(" %d", l.Status)
		}
		out += " (" + req + ")"
	}
	return out
}
