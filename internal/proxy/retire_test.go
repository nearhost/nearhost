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
// other ports can be bound at once. Which servers Caddy starts first varies,
// so the load is failed several times. The check that the wait was asked for
// does the real work: Caddy has closed the listeners by the time Apply
// returns on all but a few runs, so the bind rarely fails without the wait.
func TestAFailedLoadWaitsForTheListenersItOpened(t *testing.T) {
	var waited []map[*retireListener]struct{}
	orig := retired
	retired = func(ls map[*retireListener]struct{}, timeout time.Duration) map[*retireListener]struct{} {
		waited = append(waited, ls)
		return orig(ls, timeout)
	}
	t.Cleanup(func() { retired = orig })
	sites := []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}
	for try := 0; try < 10; try++ {
		https, http := freePort(t), freePort(t)
		held, err := netbind.Listen("tcp", netbind.ServeAddrs(https)[0])
		if err != nil {
			t.Fatal(err)
		}
		p := New(Options{StorageRoot: testutil.TempDir(t, "nh-caddy-"), HTTPSPort: https, HTTPPort: http})
		waited = nil
		err = p.Apply(sites)
		held.Close()
		if err == nil {
			p.Stop()
			t.Fatal("Apply succeeded with the HTTPS port held")
		}
		if p.Running() {
			t.Fatal("Running after a load that failed")
		}
		if len(waited) != 1 {
			t.Fatalf("the failed load waited %d times for its listeners, want once", len(waited))
		}
		if n := len(retiring.snapshot()); n != 0 {
			t.Fatalf("%d listeners open after the failed load returned", n)
		}
		for _, a := range netbind.ServeAddrs(http) {
			l, err := netbind.Listen("tcp", a)
			if err != nil {
				t.Fatalf("the HTTP port right after the failed load: %v", err)
			}
			l.Close()
		}
	}
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
