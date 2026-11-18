package engine

import (
	"context"
	"time"
)

// wakeCheckEvery is how often the wake watcher reads the wall clock.
const wakeCheckEvery = 30 * time.Second

// wakeGap is how far the wall clock must move between two readings for the
// watcher to take it that the computer slept, or that the clock was set
// ahead.
const wakeGap = 2 * time.Minute

// startWakeWatch starts the wake watcher. The ticker and the first reading
// of the clock are made here, before the watcher runs, so it measures from
// the end of Start. lifeMu must be held, so Shutdown cannot already be
// waiting for it.
func (e *Engine) startWakeWatch() {
	now, newTicker := e.opts.WakeClock.Now, e.opts.WakeClock.NewTicker
	if now == nil {
		now = time.Now
	}
	if newTicker == nil {
		newTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ticks, stop := newTicker(e.wakeEvery)
	e.wakeWork.Add(1)
	go e.watchWake(e.ctx, now, now(), ticks, stop)
}

// watchWake reads the wall clock at every tick until ctx ends, last being
// the reading before the first. Tickers do not tick while the computer
// sleeps, so a tick that finds the wall clock more than wakeGap past the
// last reading follows a sleep, and the proxy is given its sites again
// (renewAfterWake) for the certificates that came due meanwhile, once. A
// clock set back renews nothing.
func (e *Engine) watchWake(ctx context.Context, now func() time.Time, last time.Time, ticks <-chan time.Time, stop func()) {
	defer e.wakeWork.Done()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		t := now()
		gap := wallGap(last, t)
		last = t
		if gap > wakeGap {
			e.renewAfterWake(ctx)
		}
	}
}

// wallGap is how far the wall clock moved from a to b. Go compares readings
// of time.Now by their monotonic clock, which stands still while the
// computer sleeps on Linux and macOS; Round(0) drops it, so the wall clock
// is compared.
func wallGap(a, b time.Time) time.Duration {
	return b.Round(0).Sub(a.Round(0))
}

// renewAfterWake gives the proxy the published sites again through
// renewSites, under applyMu then mu like any site change, unless the engine
// is shutting down.
func (e *Engine) renewAfterWake(ctx context.Context) {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	if e.closed || ctx.Err() != nil {
		e.mu.Unlock()
		return
	}
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if _, err := e.renewSites(sites); err != nil {
		e.bus.Publish(Event{Message: "could not renew the sites' certificates after the computer woke: " + err.Error()})
	}
}
