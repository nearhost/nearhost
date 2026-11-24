package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/update"
)

const (
	// updateFirstCheck is how long after it starts the core makes its first
	// check, so a start is never held up by one.
	updateFirstCheck = time.Minute
	// updateEvery is the time between checks. cachedLatest asks GitHub at
	// most once a day, whatever the cadence.
	updateEvery = 24 * time.Hour
)

// updateWatch is the core's check for a new release. It runs for as long as
// the core does, and says once, as an event, each release newer than this
// one, so the interface, `nearhost up` and the core's log all show it
// through the stream they already follow.
type updateWatch struct {
	// current is this release, without a leading "v".
	current string
	// latest returns the latest released version.
	latest func(ctx context.Context) (string, error)
	// publish says an event where the core's events go.
	publish func(engine.Event)
	// first is the wait before the first check, every the wait between
	// the others.
	first, every time.Duration
	// sleep waits d and reports true, or returns false as soon as ctx ends.
	// nil waits on a timer. Tests replace it with a clock that does not wait.
	sleep func(ctx context.Context, d time.Duration) bool

	mu    sync.Mutex
	newer string // the newer release last found, or ""
}

// newUpdateWatch is the production watch, which asks through the daily cache.
func newUpdateWatch(publish func(engine.Event)) *updateWatch {
	return &updateWatch{
		current: strings.TrimPrefix(Version, "v"), latest: cachedLatest, publish: publish,
		first: updateFirstCheck, every: updateEvery,
	}
}

// run checks after the first wait and then at every interval, until ctx
// ends. A check that fails says nothing: the next one tries again.
func (w *updateWatch) run(ctx context.Context) {
	wait := w.first
	for w.wait(ctx, wait) {
		w.check(ctx)
		wait = w.every
	}
}

// wait is w.sleep, or a timer.
func (w *updateWatch) wait(ctx context.Context, d time.Duration) bool {
	if w.sleep != nil {
		return w.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// check asks for the latest release once, and publishes it when it is newer
// than this one and not the one last published.
func (w *updateWatch) check(ctx context.Context) {
	latest, err := w.latest(ctx)
	if err != nil || !update.Newer(w.current, latest) {
		return
	}
	w.mu.Lock()
	said := w.newer == latest
	w.newer = latest
	w.mu.Unlock()
	if said {
		return
	}
	w.publish(engine.Event{Message: updateNotice(w.current, latest)})
}

// updateNotice is what the core says, as an event, of the release latest
// newer than its own, current.
func updateNotice(current, latest string) string {
	return fmt.Sprintf("A new nearhost is available: %s → %s. Run `nearhost update` to update.", current, latest)
}

// latestNewer is the newer release found, which Status reports, or "" when
// none is known.
func (w *updateWatch) latestNewer() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.newer
}

// updateChecksAllowed reports whether this build may look for a new release
// at all: not with NEARHOST_NO_UPDATE_CHECK set, and not for a development
// build, so tests and builds from source never ask GitHub.
func updateChecksAllowed(getenv func(string) string, version string) bool {
	return getenv("NEARHOST_NO_UPDATE_CHECK") == "" && update.IsRelease(version)
}
