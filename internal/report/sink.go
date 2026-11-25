package report

import (
	"context"
	"os"
	"slices"
	"sync"
)

// Sink receives the warnings and notices reported while work goes on.
type Sink interface {
	Report(*Message)
}

// SinkFunc makes a function a Sink.
type SinkFunc func(*Message)

// Report calls f(m).
func (f SinkFunc) Report(m *Message) { f(m) }

type sinkKey struct{}

// WithSink returns a context whose reports go to s.
func WithSink(ctx context.Context, s Sink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

// To reports m to the context's sink, or to Fallback when the context has
// none, so a message is never dropped. A nil message is ignored.
func To(ctx context.Context, m *Message) {
	if m == nil {
		return
	}
	if ctx != nil {
		if s, ok := ctx.Value(sinkKey{}).(Sink); ok && s != nil {
			s.Report(m)
			return
		}
	}
	if f := Fallback; f != nil {
		f.Report(m)
	}
}

// Fallback receives messages reported to a context with no sink. It
// writes the log block to standard error; importing package term makes it
// the terminal renderer, and the core sets it to its log at start. Set it
// before goroutines report.
var Fallback Sink = SinkFunc(func(m *Message) {
	os.Stderr.WriteString(LogText(m) + "\n")
})

// Collector keeps what is reported to it, in order, such as the warnings
// an API response returns. It is safe for concurrent use.
type Collector struct {
	mu   sync.Mutex
	msgs []*Message
}

// Report keeps m.
func (c *Collector) Report(m *Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
}

// Messages returns what was reported, in order, or nil.
func (c *Collector) Messages() []*Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.msgs)
}

// Fan returns a sink that reports to each of sinks in turn. Nil sinks are
// skipped.
func Fan(sinks ...Sink) Sink {
	sinks = slices.DeleteFunc(slices.Clone(sinks), func(s Sink) bool { return s == nil })
	return SinkFunc(func(m *Message) {
		for _, s := range sinks {
			s.Report(m)
		}
	})
}
