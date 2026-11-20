package proxy

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/testutil"
)

// slowListener is a listener whose Close returns when the test lets it.
type slowListener struct {
	net.Listener
	closing chan struct{} // closed as Close begins
	release chan struct{} // Close returns once this is closed
}

func (l *slowListener) Close() error {
	close(l.closing)
	<-l.release
	return errors.New("closed late")
}

// A listener is open until the close of what it wraps has returned, and
// waitRetired waits for exactly that.
func TestRetireWaitsUntilACloseHasReturned(t *testing.T) {
	set := &retireSet{open: map[*retireListener]struct{}{}}
	kept := set.wrap(&slowListener{closing: make(chan struct{}), release: make(chan struct{})})
	before := set.snapshot()
	slow := &slowListener{closing: make(chan struct{}), release: make(chan struct{})}
	ln := set.wrap(slow)
	opened := set.since(before)
	if _, ok := opened[ln.(*retireListener)]; !ok || len(opened) != 1 {
		t.Fatalf("since = %v, want the one listener wrapped after the snapshot", opened)
	}
	if _, ok := opened[kept.(*retireListener)]; ok {
		t.Fatal("since holds a listener that was open before")
	}

	closed := make(chan error, 1)
	go func() { closed <- ln.Close() }()
	<-slow.closing
	if left := waitRetired(opened, 50*time.Millisecond); len(left) != 1 {
		t.Fatalf("waitRetired left %d listeners while the listener's close was still running, want 1", len(left))
	}
	if n := len(set.since(before)); n != 1 {
		t.Fatalf("%d listeners open during the close, want 1", n)
	}
	close(slow.release)
	if left := waitRetired(opened, 10*time.Second); len(left) != 0 {
		t.Fatal("waitRetired gave up on a listener that closed")
	}
	if err := <-closed; err == nil || err.Error() != "closed late" {
		t.Errorf("Close = %v, want the wrapped listener's error", err)
	}
	if n := len(set.since(before)); n != 0 {
		t.Errorf("%d listeners open after the close, want 0", n)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if left := waitRetired(nil, 0); len(left) != 0 {
		t.Error("waitRetired with no listeners reported a timeout")
	}
}

// A load that fails had opened some listeners before the one it could not.
// Caddy closes them in goroutines it does not wait for, and the proxy waits
// for them: when Apply returns, none is open and the failed configuration's
// other port can be bound at once. Which server Caddy starts first varies,
// and a load that meets the held port first opens nothing, so the load is
// failed, with the HTTP port and the HTTPS port held by turns, until one has
// opened a listener. The check that the wait was asked
// for does the real work: Caddy has closed the listeners by the time Apply
// returns on all but a few runs, so the set waited for is empty on most and
// the bind rarely fails without the wait.
func TestAFailedLoadWaitsForTheListenersItOpened(t *testing.T) {
	var waited []map[*retireListener]struct{}
	orig := retired
	retired = func(ls map[*retireListener]struct{}, timeout time.Duration) map[*retireListener]struct{} {
		waited = append(waited, ls)
		return orig(ls, timeout)
	}
	t.Cleanup(func() { retired = orig })
	sites := []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}
	wrapped := func() int {
		retiring.mu.Lock()
		defer retiring.mu.Unlock()
		return retiring.wrapped
	}
	var last error
	for try := 0; try < 40; try++ {
		opened := wrapped()
		https, http := freePort(t), freePort(t)
		taken, free := http, https
		if try%2 == 1 {
			taken, free = https, http
		}
		held, err := netbind.Listen("tcp", netbind.ServeAddrs(taken)[0])
		if err != nil {
			t.Fatal(err)
		}
		p := New(Options{StorageRoot: testutil.TempDir(t, "nh-caddy-"), HTTPSPort: https, HTTPPort: http})
		waited = nil
		err = p.Apply(sites)
		held.Close()
		if err == nil {
			p.Stop()
			t.Fatalf("Apply succeeded with port %d held", taken)
		}
		last = err
		if p.Running() {
			t.Fatal("Running after a load that failed")
		}
		if len(waited) != 1 {
			t.Fatalf("the failed load waited %d times for its listeners, want once", len(waited))
		}
		if n := len(retiring.snapshot()); n != 0 {
			t.Fatalf("%d listeners open after the failed load returned", n)
		}
		for _, a := range netbind.ServeAddrs(free) {
			l, err := netbind.Listen("tcp", a)
			if err != nil {
				t.Fatalf("the port that was not held, right after the failed load: %v", err)
			}
			l.Close()
		}
		if wrapped() > opened {
			return
		}
	}
	t.Fatalf("no failed load opened a listener in 40 tries, so none was waited for; the last error: %v", last)
}

// stuckListener is a listener at addr that is never closed.
type stuckListener struct {
	net.Listener
	addr net.TCPAddr
}

func (l *stuckListener) Addr() net.Addr { return &l.addr }

// stuck adds n listeners that never close to set.
func stuck(set *retireSet, n int) map[*retireListener]struct{} {
	out := map[*retireListener]struct{}{}
	for i := 0; i < n; i++ {
		ln := set.wrap(&stuckListener{addr: net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080 + i}})
		out[ln.(*retireListener)] = struct{}{}
	}
	return out
}

// standRetired makes the wait for listeners return left at once, as it does
// when they are still open after the wait, and records what was waited for.
func standRetired(t *testing.T, left func(ls map[*retireListener]struct{}) map[*retireListener]struct{}) *[]map[*retireListener]struct{} {
	t.Helper()
	var waited []map[*retireListener]struct{}
	orig := retired
	retired = func(ls map[*retireListener]struct{}, _ time.Duration) map[*retireListener]struct{} {
		waited = append(waited, ls)
		return left(ls)
	}
	t.Cleanup(func() { retired = orig })
	return &waited
}

// Listeners still open after the wait are named in the log, with their
// addresses, and forgotten, so no later load waits for them again.
func TestListenersThatDoNotCloseAreLoggedAndForgotten(t *testing.T) {
	rec := &recorder{}
	SetSink(rec)
	t.Cleanup(func() { SetSink(nil) })
	set := &retireSet{open: map[*retireListener]struct{}{}}
	kept := set.wrap(&stuckListener{addr: net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}})
	ls := stuck(set, 2)
	standRetired(t, func(ls map[*retireListener]struct{}) map[*retireListener]struct{} { return ls })

	set.settle(ls, "a load that failed")
	const want = "2 proxy listeners were still open 5s after a load that failed: 127.0.0.1:8080, 127.0.0.1:8081"
	if !rec.hasFor("", want) {
		t.Errorf("log = %q, want %q", rec.lines, want)
	}
	open := set.snapshot()
	if _, ok := open[kept.(*retireListener)]; !ok || len(open) != 1 {
		t.Errorf("%d listeners recorded as open, want the one that was not waited for", len(open))
	}

	// One that closes in time is neither logged nor forgotten early.
	one := stuck(set, 1)
	standRetired(t, func(map[*retireListener]struct{}) map[*retireListener]struct{} { return nil })
	set.settle(one, "a reload")
	if n := rec.countFor("", "still open"); n != 1 {
		t.Errorf("%d lines about open listeners, want 1", n)
	}
	if len(set.snapshot()) != 2 {
		t.Errorf("%d listeners recorded as open, want 2", len(set.snapshot()))
	}
}

// A failed load whose listeners do not close in time says so and returns
// the load's error.
func TestAFailedLoadSaysWhichListenersDidNotClose(t *testing.T) {
	rec := &recorder{}
	SetSink(rec)
	t.Cleanup(func() { SetSink(nil) })
	left := stuck(retiring, 1)
	t.Cleanup(func() {
		retiring.mu.Lock()
		defer retiring.mu.Unlock()
		for l := range left {
			delete(retiring.open, l)
		}
	})
	standRetired(t, func(map[*retireListener]struct{}) map[*retireListener]struct{} { return left })
	https, http := freePort(t), freePort(t)
	held, err := netbind.Listen("tcp", netbind.ServeAddrs(https)[0])
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	p := New(Options{StorageRoot: testutil.TempDir(t, "nh-caddy-"), HTTPSPort: https, HTTPPort: http})
	if err := p.Apply([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}); err == nil {
		p.Stop()
		t.Fatal("Apply succeeded with the HTTPS port held")
	}
	const want = "1 proxy listener was still open 5s after a load that failed: 127.0.0.1:8080"
	if !rec.hasFor("", want) {
		t.Errorf("log = %q, want %q", rec.lines, want)
	}
	if _, ok := retiring.snapshot()[firstOf(left)]; ok {
		t.Error("the listener that did not close is still waited for")
	}
}

func firstOf(ls map[*retireListener]struct{}) *retireListener {
	for l := range ls {
		return l
	}
	return nil
}

// A reload waits for the listeners of the configuration it replaces: when
// Apply returns none of them is open, so no new connection can reach one. A
// load of the configuration that already runs closes nothing and waits for
// nothing.
func TestAReloadWaitsForTheOldListeners(t *testing.T) {
	upA, upB := upstream(t, "a"), upstream(t, "b")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(upA.Server)}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(upB.Server)}
	h := startProxy(t, []Site{a})
	old := retiring.snapshot()
	if len(old) == 0 {
		t.Fatal("no listener is recorded for a running proxy")
	}
	var waited []map[*retireListener]struct{}
	orig := retired
	retired = func(ls map[*retireListener]struct{}, timeout time.Duration) map[*retireListener]struct{} {
		waited = append(waited, ls)
		return orig(ls, timeout)
	}
	t.Cleanup(func() { retired = orig })

	if err := h.p.Apply([]Site{a, b}); err != nil {
		t.Fatal(err)
	}
	if len(waited) != 1 || len(waited[0]) != len(old) {
		t.Fatalf("the reload waited for %v, want once for the %d old listeners", waited, len(old))
	}
	for l := range old {
		if _, ok := waited[0][l]; !ok {
			t.Fatal("the reload did not wait for one of the old listeners")
		}
		select {
		case <-l.closed:
		default:
			t.Fatal("an old listener was still open when the reload returned")
		}
	}
	now := retiring.snapshot()
	if len(now) != len(old) {
		t.Fatalf("%d listeners open after the reload, want %d", len(now), len(old))
	}

	waited = nil
	if err := h.p.Apply([]Site{a, b}); err != nil {
		t.Fatal(err)
	}
	if len(waited) != 0 {
		t.Errorf("a load of the running configuration waited for %d listeners", len(waited[0]))
	}
	if after := retiring.snapshot(); len(retiring.since(now)) != 0 || len(after) != len(now) {
		t.Errorf("a load of the running configuration changed the open listeners")
	}
}

// A reload whose old listeners do not close in time says so, applies the
// configuration all the same, and the next reload does not wait for them
// again.
func TestAReloadSaysWhichListenersDidNotClose(t *testing.T) {
	upA, upB := upstream(t, "a"), upstream(t, "b")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(upA.Server)}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(upB.Server)}
	h := startProxy(t, []Site{a})
	left := stuck(retiring, 2)
	t.Cleanup(func() {
		retiring.mu.Lock()
		defer retiring.mu.Unlock()
		for l := range left {
			delete(retiring.open, l)
		}
	})
	waited := standRetired(t, func(ls map[*retireListener]struct{}) map[*retireListener]struct{} {
		out := map[*retireListener]struct{}{}
		for l := range left {
			if _, ok := ls[l]; ok {
				out[l] = struct{}{}
			}
		}
		return out
	})

	if err := h.p.Apply([]Site{a, b}); err != nil {
		t.Fatal(err)
	}
	const want = "2 proxy listeners were still open 5s after a reload: 127.0.0.1:8080, 127.0.0.1:8081"
	if !h.rec.hasFor("", want) {
		t.Errorf("the log lacks %q", want)
	}
	if !h.p.Running() {
		t.Error("not Running after a reload whose old listeners stayed open")
	}
	if err := h.p.Apply([]Site{a}); err != nil {
		t.Fatal(err)
	}
	if len(*waited) != 2 {
		t.Fatalf("waited %d times over two reloads, want twice", len(*waited))
	}
	for l := range left {
		if _, ok := (*waited)[1][l]; ok {
			t.Fatal("the second reload waited again for a listener that had not closed")
		}
	}
	if n := h.rec.countFor("", "still open"); n != 1 {
		t.Errorf("%d lines about open listeners, want 1", n)
	}
}

// A reload that takes the last site away opens no listener and closes the
// old ones: it waits for them as any reload does, so one still open after
// the wait is said in the log and not waited for again.
func TestAReloadToNoSitesSettlesTheOldListeners(t *testing.T) {
	up := upstream(t, "a")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}
	h := startProxy(t, []Site{a})
	old := retiring.snapshot()
	if len(old) == 0 {
		t.Fatal("no listener is recorded for a running proxy")
	}
	left := stuck(retiring, 1)
	t.Cleanup(func() {
		retiring.mu.Lock()
		defer retiring.mu.Unlock()
		for l := range left {
			delete(retiring.open, l)
		}
	})
	waited := standRetired(t, func(ls map[*retireListener]struct{}) map[*retireListener]struct{} {
		out := map[*retireListener]struct{}{}
		for l := range left {
			if _, ok := ls[l]; ok {
				out[l] = struct{}{}
			}
		}
		return out
	})

	if err := h.p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if len(*waited) != 1 {
		t.Fatalf("the reload to no sites waited %d times for the old listeners, want once", len(*waited))
	}
	for l := range old {
		if _, ok := (*waited)[0][l]; !ok {
			t.Fatal("the reload to no sites did not wait for one of the old listeners")
		}
	}
	const want = "1 proxy listener was still open 5s after a reload: 127.0.0.1:8080"
	if !h.rec.hasFor("", want) {
		t.Errorf("the log lacks %q", want)
	}
	if _, ok := retiring.snapshot()[firstOf(left)]; ok {
		t.Error("the listener that did not close is still waited for")
	}

	if err := h.p.Apply([]Site{a}); err != nil {
		t.Fatal(err)
	}
	for _, ls := range (*waited)[1:] {
		if _, ok := ls[firstOf(left)]; ok {
			t.Fatal("the next reload waited again for the listener that had not closed")
		}
	}
	if n := h.rec.countFor("", "still open"); n != 1 {
		t.Errorf("%d lines about open listeners, want 1", n)
	}
}

// Stop waits for the listeners it closes, as a load does: when it returns
// none of them is open, so their ports can be bound at once.
func TestAStopWaitsForItsListeners(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}})
	old := retiring.snapshot()
	if len(old) == 0 {
		t.Fatal("no listener is recorded for a running proxy")
	}
	var waited []map[*retireListener]struct{}
	orig := retired
	retired = func(ls map[*retireListener]struct{}, timeout time.Duration) map[*retireListener]struct{} {
		waited = append(waited, ls)
		return orig(ls, timeout)
	}
	t.Cleanup(func() { retired = orig })

	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(waited) != 1 || len(waited[0]) != len(old) {
		t.Fatalf("Stop waited for %v, want once for the %d listeners", waited, len(old))
	}
	for l := range old {
		if _, ok := waited[0][l]; !ok {
			t.Fatal("Stop did not wait for one of the listeners")
		}
		select {
		case <-l.closed:
		default:
			t.Fatal("a listener was still open when Stop returned")
		}
	}
	if n := len(retiring.snapshot()); n != 0 {
		t.Errorf("%d listeners open after Stop", n)
	}
}

// A stop whose listeners do not close in time says so and forgets them, so
// the next start does not take them for listeners its load replaced and
// wait for them again.
func TestAStopSaysWhichListenersDidNotClose(t *testing.T) {
	up := upstream(t, "a")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}
	h := startProxy(t, []Site{a})
	left := stuck(retiring, 1)
	t.Cleanup(func() {
		retiring.mu.Lock()
		defer retiring.mu.Unlock()
		for l := range left {
			delete(retiring.open, l)
		}
	})
	waited := standRetired(t, func(ls map[*retireListener]struct{}) map[*retireListener]struct{} {
		out := map[*retireListener]struct{}{}
		for l := range left {
			if _, ok := ls[l]; ok {
				out[l] = struct{}{}
			}
		}
		return out
	})

	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(*waited) != 1 {
		t.Fatalf("Stop waited %d times for its listeners, want once", len(*waited))
	}
	const want = "1 proxy listener was still open 5s after a stop: 127.0.0.1:8080"
	if !h.rec.hasFor("", want) {
		t.Errorf("the log lacks %q", want)
	}
	if _, ok := retiring.snapshot()[firstOf(left)]; ok {
		t.Fatal("the listener that did not close is still waited for")
	}

	if err := h.p.Apply([]Site{a}); err != nil {
		t.Fatal(err)
	}
	for _, ls := range (*waited)[1:] {
		if _, ok := ls[firstOf(left)]; ok {
			t.Fatal("the next start waited again for the listener that had not closed")
		}
	}
	if n := h.rec.countFor("", "still open"); n != 1 {
		t.Errorf("%d lines about open listeners, want 1", n)
	}
}
