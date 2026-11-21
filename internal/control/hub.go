package control

import "sync"

// RingSize is how many envelopes the hub keeps for clients that reconnect.
// A client that asks for older ones is sent a reset.
const RingSize = 4096

// streamBuffer is how many envelopes may wait for each stream. A stream
// that falls further behind is sent a reset in their place.
const streamBuffer = 256

// Hub numbers the envelopes the core publishes, keeps the last RingSize of
// them, and hands them to the streams attached. Publish never waits for a
// stream. It is safe for concurrent use.
type Hub struct {
	// attachMu orders attaching and detaching, so the OnAttached hook is
	// told of each change in turn, without holding up Publish.
	attachMu   sync.Mutex
	onAttached func(attached bool)

	mu   sync.Mutex
	seq  uint64     // the number of the last envelope published
	ring []Envelope // envelope n is at (n-1) % RingSize
	subs map[*sub]struct{}
}

// sub is one attached stream: the envelopes waiting for it, at most
// streamBuffer of them.
type sub struct {
	// ready is signalled when queue has gained envelopes.
	ready chan struct{}

	mu    sync.Mutex
	queue []Envelope
}

// put adds e to the queue. When the queue is full, everything in it is
// dropped and a reset to e's number takes its place: the reader takes the
// queue whole, so it never sends an envelope that comes after a gap.
func (s *sub) put(e Envelope) {
	s.mu.Lock()
	if len(s.queue) < streamBuffer {
		s.queue = append(s.queue, e)
	} else {
		clear(s.queue)
		s.queue = append(s.queue[:0], Envelope{Seq: e.Seq, Kind: KindReset})
	}
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// take returns the envelopes waiting, oldest first, and empties the queue.
func (s *sub) take() []Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queue
	s.queue = nil
	return q
}

// NewHub returns a hub that has published nothing.
func NewHub() *Hub {
	return &Hub{ring: make([]Envelope, RingSize), subs: map[*sub]struct{}{}}
}

// Publish numbers e, keeps it, queues it for every stream, and returns its
// number. A stream whose queue is full has the envelopes waiting for it
// dropped, and is sent a reset to e's number in their place.
func (h *Hub) Publish(e Envelope) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.Seq = h.seq
	h.ring[(e.Seq-1)%RingSize] = e
	for s := range h.subs {
		s.put(e)
	}
	return e.Seq
}

// Seq returns the number of the last envelope published, 0 before the first.
func (h *Hub) Seq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// Clients returns how many streams are attached.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// OnAttached sets f to be told when the number of attached streams goes
// from 0 to 1 (true) and back to 0 (false). It replaces any f set before.
// f is called in the request that attaches or detaches, and must not call
// back into the hub's attaching.
func (h *Hub) OnAttached(f func(attached bool)) {
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	h.onAttached = f
}

// subscribe attaches a stream that wants the envelopes after after. It
// returns the stream and the envelopes it has already missed, which go out
// before any in its queue. When the hub no longer has all of those, or
// has never published as many, the backlog is a reset to the last number
// instead.
func (h *Hub) subscribe(after uint64) (*sub, []Envelope) {
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	h.mu.Lock()
	s := &sub{ready: make(chan struct{}, 1)}
	h.subs[s] = struct{}{}
	n := len(h.subs)
	var backlog []Envelope
	oldest := uint64(1)
	if h.seq > RingSize {
		oldest = h.seq - RingSize + 1
	}
	switch {
	case after > h.seq || after+1 < oldest:
		backlog = []Envelope{{Seq: h.seq, Kind: KindReset}}
	case after < h.seq:
		backlog = make([]Envelope, 0, h.seq-after)
		for seq := after + 1; seq <= h.seq; seq++ {
			backlog = append(backlog, h.ring[(seq-1)%RingSize])
		}
	}
	h.mu.Unlock()
	if n == 1 && h.onAttached != nil {
		h.onAttached(true)
	}
	return s, backlog
}

// unsubscribe detaches s.
func (h *Hub) unsubscribe(s *sub) {
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	h.mu.Lock()
	delete(h.subs, s)
	n := len(h.subs)
	h.mu.Unlock()
	if n == 0 && h.onAttached != nil {
		h.onAttached(false)
	}
}
