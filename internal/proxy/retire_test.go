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
	if waitRetired(opened, 50*time.Millisecond) {
		t.Fatal("waitRetired returned true while the listener's close was still running")
	}
	if n := len(set.since(before)); n != 1 {
		t.Fatalf("%d listeners open during the close, want 1", n)
	}
	close(slow.release)
	if !waitRetired(opened, 10*time.Second) {
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
	if !waitRetired(nil, 0) {
		t.Error("waitRetired with no listeners reported a timeout")
	}
}

// A load that fails had opened some listeners before the one it could not.
// Caddy closes them in goroutines it does not wait for, and the proxy waits
// for them: when Apply returns, none is open and the failed configuration's
// other ports can be bound at once. Which servers Caddy starts first varies,
// so the load is failed several times.
func TestAFailedLoadWaitsForTheListenersItOpened(t *testing.T) {
	var waited []map[*retireListener]struct{}
	orig := retired
	retired = func(ls map[*retireListener]struct{}, timeout time.Duration) bool {
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
