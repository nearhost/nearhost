package report

import (
	"context"
	"slices"
	"testing"
)

func TestToWithoutSinkUsesFallback(t *testing.T) {
	var got []*Message
	old := Fallback
	Fallback = SinkFunc(func(m *Message) { got = append(got, m) })
	t.Cleanup(func() { Fallback = old })

	m := Warnf(HostsPublic, "w")
	To(context.Background(), m)
	if len(got) != 1 || got[0] != m {
		t.Fatalf("fallback got %v", got)
	}

	c := &Collector{}
	To(WithSink(context.Background(), c), m)
	if len(got) != 1 || len(c.Messages()) != 1 {
		t.Fatal("a context with a sink still went to the fallback")
	}

	To(context.Background(), nil)
	if len(got) != 1 {
		t.Fatal("a nil message was reported")
	}
}

func TestCollectorKeepsOrder(t *testing.T) {
	c := &Collector{}
	a, b, d := Warnf(HostsPublic, "a"), Notef(PresetsNote, "b"), Warnf(HostsOverlap, "d")
	for _, m := range []*Message{a, b, d} {
		c.Report(m)
	}
	got := c.Messages()
	if !slices.Equal(got, []*Message{a, b, d}) {
		t.Fatalf("Messages = %v", got)
	}
	got[0] = nil
	if c.Messages()[0] != a {
		t.Fatal("Messages returned the collector's own slice")
	}
	if (&Collector{}).Messages() != nil {
		t.Fatal("an empty collector has messages")
	}
}

func TestFanReportsToEach(t *testing.T) {
	a, b := &Collector{}, &Collector{}
	s := Fan(a, nil, b)
	m := Warnf(HostsPublic, "w")
	s.Report(m)
	if len(a.Messages()) != 1 || len(b.Messages()) != 1 {
		t.Fatalf("a=%d b=%d", len(a.Messages()), len(b.Messages()))
	}
}
