package control

import (
	"sync"
	"testing"
	"time"
)

func TestHubNumbersFromOne(t *testing.T) {
	h := NewHub()
	for want := uint64(1); want <= 3; want++ {
		if got := h.Publish(Envelope{Kind: KindEvent}); got != want {
			t.Fatalf("Publish = %d, want %d", got, want)
		}
	}
	if got := h.Seq(); got != 3 {
		t.Fatalf("Seq = %d, want 3", got)
	}
}

// A subscription that asks for what the hub has no longer, or for more than
// it has ever had, starts with a reset to the hub's last number.
func TestSubscribeBacklog(t *testing.T) {
	h := NewHub()
	for range RingSize + 2 {
		h.Publish(Envelope{Kind: KindEvent})
	}
	last := h.Seq()
	for _, tc := range []struct {
		after     uint64
		wantFirst uint64
		wantKind  string
		wantLen   int
	}{
		{after: last - 1, wantFirst: last, wantKind: KindEvent, wantLen: 1},
		{after: last, wantLen: 0},
		{after: 2, wantFirst: 3, wantKind: KindEvent, wantLen: RingSize},
		{after: 1, wantFirst: last, wantKind: KindReset, wantLen: 1},
		{after: last + 5, wantFirst: last, wantKind: KindReset, wantLen: 1},
	} {
		s, backlog := h.subscribe(tc.after)
		h.unsubscribe(s)
		if len(backlog) != tc.wantLen {
			t.Errorf("after %d: %d in the backlog, want %d", tc.after, len(backlog), tc.wantLen)
			continue
		}
		if tc.wantLen == 0 {
			continue
		}
		if backlog[0].Seq != tc.wantFirst || backlog[0].Kind != tc.wantKind {
			t.Errorf("after %d: first is %d %q, want %d %q", tc.after, backlog[0].Seq, backlog[0].Kind, tc.wantFirst, tc.wantKind)
		}
		for i := 1; i < len(backlog); i++ {
			if backlog[i].Seq != backlog[i-1].Seq+1 {
				t.Errorf("after %d: %d follows %d", tc.after, backlog[i].Seq, backlog[i-1].Seq)
				break
			}
		}
	}
}

// attachLog records the calls of an OnAttached hook.
type attachLog struct {
	mu    sync.Mutex
	calls []bool
}

func (l *attachLog) hook(attached bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, attached)
}

func (l *attachLog) get() []bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]bool(nil), l.calls...)
}

// A watch is told that something was published, but is not a stream: the
// hub does not count it, nor tell the OnAttached hook of it, and Publish
// never waits for it.
func TestHubWatchIsNotAClient(t *testing.T) {
	h := NewHub()
	var log attachLog
	h.OnAttached(log.hook)
	changed, stop := h.Watch()
	defer stop()
	if n := h.Clients(); n != 0 {
		t.Errorf("Clients = %d with a watch open, want 0", n)
	}
	published := make(chan struct{})
	go func() {
		defer close(published)
		for range 3 {
			h.Publish(Envelope{Kind: KindEvent})
		}
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish waited for the watch")
	}
	select {
	case <-changed:
	default:
		t.Fatal("the watch was not signalled")
	}
	if calls := log.get(); len(calls) != 0 {
		t.Errorf("OnAttached was told %v", calls)
	}
	// Once stopped, a watch is signalled no more.
	stop()
	h.Publish(Envelope{Kind: KindEvent})
	select {
	case <-changed:
		t.Error("a stopped watch was signalled")
	default:
	}
}
