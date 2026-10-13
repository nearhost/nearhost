package proxy

import (
	"bytes"
	"io"
	"sync"

	"github.com/caddyserver/caddy/v2"
)

// LogSink receives one log line at a time. alias is empty for Caddy's own
// (non-access) logs.
type LogSink interface {
	Log(alias, line string)
}

// LogFunc adapts a function to LogSink.
type LogFunc func(alias, line string)

// Log implements LogSink.
func (f LogFunc) Log(alias, line string) { f(alias, line) }

// Caddy is process-global, so the sink its log writers forward to is too. It
// is safe for concurrent use; SetSink(nil) restores the discarding default.
var (
	sinkMu     sync.RWMutex
	globalSink LogSink = discard
)

var discard = LogFunc(func(string, string) {})

// SetSink routes all Caddy log output to s. Passing nil discards it.
func SetSink(s LogSink) {
	if s == nil {
		s = discard
	}
	sinkMu.Lock()
	defer sinkMu.Unlock()
	globalSink = s
}

func emit(alias, line string) {
	sinkMu.RLock()
	s := globalSink
	sinkMu.RUnlock()
	s.Log(alias, line)
}

// The module registry is global and rejects duplicates, so registration
// happens exactly once, at package init.
func init() {
	caddy.RegisterModule(Writer{})
}

// Writer is a Caddy log writer module that forwards lines to the sink.
type Writer struct {
	Alias string `json:"alias,omitempty"`
}

// CaddyModule implements caddy.Module.
func (Writer) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.logging.writers." + writerModule,
		New: func() caddy.Module { return new(Writer) },
	}
}

// String implements fmt.Stringer for Caddy's writer description.
func (w Writer) String() string { return "nearhost:" + w.Alias }

// WriterKey implements caddy.WriterOpener.
func (w Writer) WriterKey() string { return "nearhost:" + w.Alias }

// OpenWriter implements caddy.WriterOpener.
func (w Writer) OpenWriter() (io.WriteCloser, error) {
	return &lineWriter{alias: w.Alias}, nil
}

type lineWriter struct {
	alias string
	mu    sync.Mutex
	buf   bytes.Buffer
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line) // keep the partial line
			break
		}
		emit(l.alias, line[:len(line)-1])
	}
	return len(p), nil
}

func (l *lineWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() > 0 {
		emit(l.alias, l.buf.String())
		l.buf.Reset()
	}
	return nil
}

var _ caddy.WriterOpener = Writer{}
