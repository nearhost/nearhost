package testutil

import (
	"sync"
	"testing"
	"time"
)

// FakeClock stands in for the clock, the timer and the ticker of a
// transport's wait after logins were refused. Its time starts at 01:49
// UTC, which is 14:49 in its zone, and moves only when a test moves it.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	zone    *time.Location
	waits   []time.Duration // every timer asked for, in order
	stopped int             // timers stopped before they fired
	timer   chan time.Time  // the timer under way, or nil
	ticker  chan time.Time  // the ticker under way, or nil
	tickers int             // tickers started and not stopped
}

// NewFakeClock returns a FakeClock at 2026-11-18 14:49 in a zone 13 hours
// ahead of UTC.
func NewFakeClock() *FakeClock {
	return &FakeClock{
		now:  time.Date(2026, 11, 18, 1, 49, 0, 0, time.UTC),
		zone: time.FixedZone("NZDT", 13*60*60),
	}
}

// Now is the clock's time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Zone is the clock's time zone.
func (c *FakeClock) Zone() *time.Location { return c.zone }

// NewTimer starts a timer that fires only when Pass is called.
func (c *FakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waits = append(c.waits, d)
	c.timer = ch
	return ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.timer != ch {
			return false
		}
		c.timer = nil
		c.stopped++
		return true
	}
}

// NewTicker starts a ticker that ticks only when Tick is called.
func (c *FakeClock) NewTicker(time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time)
	c.ticker = ch
	c.tickers++
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.ticker == ch {
			c.ticker = nil
		}
		c.tickers--
	}
}

// Waits returns the timers asked for so far.
func (c *FakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// Stopped returns how many timers were stopped before they fired.
func (c *FakeClock) Stopped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// Tickers returns how many tickers run.
func (c *FakeClock) Tickers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tickers
}

// Pass fires the timer under way, the clock moving on by its length.
func (c *FakeClock) Pass(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer == nil {
		t.Fatal("no timer is under way")
	}
	c.now = c.now.Add(c.waits[len(c.waits)-1])
	c.timer <- c.now
	c.timer = nil
}

// Jump moves the clock on by d and fires nothing, as a computer's sleep
// moves the wall clock and not its timers.
func (c *FakeClock) Jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Tick ticks the ticker under way once, and waits until it is received.
func (c *FakeClock) Tick(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	ch, now := c.ticker, c.now
	c.mu.Unlock()
	if ch == nil {
		t.Fatal("no ticker is under way")
	}
	select {
	case ch <- now:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing received the tick")
	}
}
