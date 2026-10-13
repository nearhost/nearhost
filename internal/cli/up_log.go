package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// proxyLine is the part of a Caddy JSON log line that `up` shows.
type proxyLine struct {
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Error   string `json:"error"`
	Request *struct {
		Method string `json:"method"`
		URI    string `json:"uri"`
	} `json:"request"`
	Status   int     `json:"status"`
	Duration float64 `json:"duration"` // seconds
}

// plainLog turns a log line of the proxy, which writes JSON, into a short
// plain one: "GET /path 200 in 12ms" for a handled request, otherwise
// "<level>: <message>". Any other line is returned as it is.
func plainLog(line string) string {
	if !strings.HasPrefix(line, "{") {
		return line
	}
	var l proxyLine
	if err := json.Unmarshal([]byte(line), &l); err != nil || l.Msg == "" {
		return line
	}
	if l.Request != nil && l.Status != 0 {
		took := time.Duration(l.Duration * float64(time.Second)).Round(time.Millisecond)
		return fmt.Sprintf("%s %s %d in %s", l.Request.Method, l.Request.URI, l.Status, took)
	}
	out := l.Msg
	if l.Level != "" {
		out = l.Level + ": " + out
	}
	if l.Error != "" {
		out += ": " + l.Error
	}
	if l.Request != nil {
		out += fmt.Sprintf(" (%s %s)", l.Request.Method, l.Request.URI)
	}
	return out
}
