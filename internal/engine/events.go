// Package engine starts and stops projects by wiring transport, hosts,
// forwards and the proxy together.
package engine

import (
	"sync"
	"time"
)

// Event is a log line or a state change attributed to a project or server.
//
// There are three kinds:
//   - a project state change: Project and State are set, Server names the
//     project's server unless it is local, and Message reads "state: <state>"
//     followed by ": <reason>" when there is one;
//   - a project log line: Project is set and State is empty. Proxy access
//     lines and forward errors arrive this way;
//   - a server or proxy log line: Project is empty. Server is set for the
//     messages of that server's SSH supervisor and empty for the proxy's own
//     messages. A project's log is its own lines plus those of its server.
type Event struct {
	Time    time.Time
	Project string // empty for server-level and proxy-level events
	Server  string // empty for local projects and proxy-level events
	State   State  // the new state; empty unless this is a state change
	Message string
}

// Bus fans events out to subscribers. A subscriber whose buffer is full
// misses events rather than blocking the engine. It is safe for concurrent
// use.
type Bus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

// NewBus creates an empty bus.
func NewBus() *Bus {
	return &Bus{subs: map[int]chan Event{}}
}

// Subscribe returns a channel of events with the given buffer size and a
// function that unsubscribes and closes the channel. The function may be
// called more than once.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 0 {
		buffer = 0
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

// Publish delivers e to every subscriber that has room, without blocking. A
// zero Time is set to now.
func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
