package cli

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
)

// scriptedWatch runs a watch whose latest answers from answers, one per
// check, and whose clock never waits: sleep records how long it was asked to
// wait and ends the run once the answers are used up. It returns the events
// published, the waits asked for and the watch.
func scriptedWatch(t *testing.T, current string, answers []string, fail error) (*updateWatch, []engine.Event, []time.Duration) {
	t.Helper()
	var (
		mu     sync.Mutex
		events []engine.Event
		waits  []time.Duration
		checks int
	)
	w := &updateWatch{
		current: current,
		first:   time.Minute,
		every:   24 * time.Hour,
		latest: func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			i := checks
			checks++
			if fail != nil {
				return "", fail
			}
			return answers[i], nil
		},
		publish: func(ev engine.Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
		},
	}
	total := len(answers)
	if fail != nil {
		total = 3
	}
	w.sleep = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return checks < total
	}
	w.run(context.Background())
	return w, events, waits
}

func TestUpdateWatchPublishesOnce(t *testing.T) {
	w, events, waits := scriptedWatch(t, "0.15.0", []string{"0.16.0", "0.16.0", "0.16.0"}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %v, want one", events)
	}
	want := "A new nearhost is available: 0.15.0 → 0.16.0."
	if events[0].Message != want || events[0].Report == nil || events[0].Report.Code != report.UpdateAvailable {
		t.Errorf("message = %q, want %q", events[0].Message, want)
	}
	if got := w.latestNewer(); got != "0.16.0" {
		t.Errorf("latestNewer = %q, want 0.16.0", got)
	}
	if want := []time.Duration{time.Minute, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour}; !reflect.DeepEqual(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
}

// A release newer than the one said is another event.
func TestUpdateWatchPublishesAgainForAnotherRelease(t *testing.T) {
	var events []engine.Event
	w := &updateWatch{
		current: "0.15.0",
		publish: func(ev engine.Event) { events = append(events, ev) },
	}
	for _, v := range []string{"0.16.0", "0.16.0", "0.16.1"} {
		w.latest = func(context.Context) (string, error) { return v, nil }
		w.check(context.Background())
	}
	if len(events) != 2 {
		t.Fatalf("events = %v, want two", events)
	}
	if got := w.latestNewer(); got != "0.16.1" {
		t.Errorf("latestNewer = %q, want 0.16.1", got)
	}
}

func TestUpdateWatchSilentOnError(t *testing.T) {
	w, events, _ := scriptedWatch(t, "0.15.0", nil, errors.New("offline"))
	if len(events) != 0 {
		t.Errorf("events = %v, want none", events)
	}
	if got := w.latestNewer(); got != "" {
		t.Errorf("latestNewer = %q, want none", got)
	}
}

func TestUpdateWatchNotNewer(t *testing.T) {
	for _, latest := range []string{"0.15.0", "0.14.0"} {
		w, events, _ := scriptedWatch(t, "0.15.0", []string{latest}, nil)
		if len(events) != 0 || w.latestNewer() != "" {
			t.Errorf("latest %s: events %v, newer %q", latest, events, w.latestNewer())
		}
	}
}

// The watch ends with its context, even while it waits for the next check.
func TestUpdateWatchStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &updateWatch{
		current: "0.15.0", first: time.Hour, every: time.Hour,
		latest:  func(context.Context) (string, error) { return "", errors.New("never asked") },
		publish: func(engine.Event) {},
	}
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not stop with its context")
	}
}

func TestUpdateChecksAllowed(t *testing.T) {
	none := func(string) string { return "" }
	off := func(k string) string {
		if k == "NEARHOST_NO_UPDATE_CHECK" {
			return "1"
		}
		return ""
	}
	if updateChecksAllowed(off, "0.15.0") {
		t.Error("allowed with NEARHOST_NO_UPDATE_CHECK set")
	}
	if updateChecksAllowed(none, "(devel)") {
		t.Error("allowed for a development build")
	}
	if !updateChecksAllowed(none, "0.15.0") {
		t.Error("refused for a release")
	}
}
