package proxy

import (
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/netbind"
)

// Caddy's Stop can return a moment before its servers have closed their
// listeners: it starts their shutdowns in goroutines and does not wait for
// them. waitReleased covers that moment: it returns once the ports can be
// bound, and says so when they cannot be in time.
func TestWaitReleased(t *testing.T) {
	port := freePort(t)
	held, err := netbind.Listen("tcp", netbind.ServeAddrs(port)[0])
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		held.Close()
	}()
	start := time.Now()
	if err := waitReleased([]listenPort{{port, netbind.ServeAddrs(port)}}, 5*time.Second); err != nil {
		t.Fatalf("waitReleased: %v", err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("returned after %s, before the port was released", waited)
	}
	if l, err := net.Listen("tcp", fmt.Sprintf(":%d", port)); err != nil {
		t.Errorf("port %d not free after waitReleased: %v", port, err)
	} else {
		l.Close()
	}

	stuck, err := netbind.Listen("tcp", netbind.ServeAddrs(port)[0])
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()
	if err := waitReleased([]listenPort{{port, netbind.ServeAddrs(port)}}, 300*time.Millisecond); err == nil || err.Error() != fmt.Sprintf("port %d is still in use after the proxy stopped", port) {
		t.Errorf("waitReleased on a held port = %v", err)
	}
}

// The proxy waits for each port at the addresses it listens on it: the
// page's port at the loopback addresses, which a wait at the wildcard
// address would not see held on macOS.
func TestPortsAreWaitedForWhereTheyListen(t *testing.T) {
	p := New(Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, WebPort: 6327})
	want := []listenPort{
		{8443, netbind.ServeAddrs(8443)},
		{8080, netbind.ServeAddrs(8080)},
		{6327, netbind.LoopbackAddrs(6327)},
	}
	if got := p.ports(); !reflect.DeepEqual(got, want) {
		t.Errorf("ports = %v, want %v", got, want)
	}
}

// A proxy with no sites listens on nothing, so stopping it waits for no
// port, even one something else holds.
func TestStopWithoutSitesWaitsForNoPort(t *testing.T) {
	https, http := freePort(t), freePort(t)
	for _, port := range []int{https, http} {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
	}
	p := New(Options{StorageRoot: t.TempDir(), HTTPSPort: https, HTTPPort: http})
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Stop took %s waiting for ports it never listened on", took)
	}
}

// Reloading with no sites stops the listeners as Stop does, and Caddy's
// reload has the same moment after it when a listener is still open: the
// proxy waits for its ports then too, so it never takes for released what
// Caddy still holds. (The engine reloads with no sites when the last
// project stops, before it stops the proxy.)
func TestApplyWithoutSitesWaitsForThePorts(t *testing.T) {
	var waited [][]listenPort
	orig := releasePorts
	releasePorts = func(ports []listenPort, timeout time.Duration) error {
		waited = append(waited, ports)
		return orig(ports, timeout)
	}
	t.Cleanup(func() { releasePorts = orig })
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
	if err := h.p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if len(waited) != 1 || waited[0][0].port != h.httpsPort || waited[0][1].port != h.httpPort {
		t.Fatalf("waited for %v after the last site went, want [%d %d]", waited, h.httpsPort, h.httpPort)
	}
	if err := h.p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(waited) != 1 {
		t.Errorf("waited again for ports nothing listened on: %v", waited)
	}
}
