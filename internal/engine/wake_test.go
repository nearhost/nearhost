package engine

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// wakeHarness is a started engine whose wake watcher runs on a fake clock,
// counting the times it gives the proxy its sites again.
func wakeHarness(t *testing.T) (*harness, *testutil.FakeClock, *atomic.Int32) {
	t.Helper()
	clock := testutil.NewFakeClock()
	h := buildHarness(t, localConfig("myapp"), func(o *Options) {
		o.WakeClock = transport.Clock{Now: clock.Now, NewTicker: clock.NewTicker}
	})
	var renews atomic.Int32
	h.eng.renewSites = func([]proxy.Site) (bool, error) {
		renews.Add(1)
		return false, nil
	}
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	return h, clock, &renews
}

// tickTwice ticks the watcher twice: the second tick is received only once
// the watcher has dealt with the first.
func tickTwice(t *testing.T, clock *testutil.FakeClock) {
	t.Helper()
	clock.Tick(t)
	clock.Tick(t)
}

// A wall clock that jumps 10 minutes between two ticks, as it does over a
// sleep, gives the proxy its sites again once; ticks that follow do not.
func TestAClockJumpRenewsOnce(t *testing.T) {
	_, clock, renews := wakeHarness(t)
	clock.Jump(10 * time.Minute)
	tickTwice(t, clock)
	if n := renews.Load(); n != 1 {
		t.Fatalf("renewed %d times after a 10-minute jump, want 1", n)
	}
	tickTwice(t, clock)
	if n := renews.Load(); n != 1 {
		t.Fatalf("renewed %d times in all, want 1", n)
	}
}

// Ticks with no jump, a gap of up to 2 minutes, and a clock set back renew
// nothing.
func TestNoForwardJumpRenewsNothing(t *testing.T) {
	_, clock, renews := wakeHarness(t)
	tickTwice(t, clock)
	clock.Jump(2 * time.Minute)
	tickTwice(t, clock)
	clock.Jump(-time.Hour)
	tickTwice(t, clock)
	if n := renews.Load(); n != 0 {
		t.Fatalf("renewed %d times, want 0", n)
	}
}

// Shutdown stops the watcher and its ticker.
func TestShutdownStopsTheWakeWatcher(t *testing.T) {
	h, clock, _ := wakeHarness(t)
	if n := clock.Tickers(); n != 1 {
		t.Fatalf("%d tickers run after Start, want 1", n)
	}
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	if n := clock.Tickers(); n != 0 {
		t.Fatalf("%d tickers run after Shutdown, want 0", n)
	}
}
