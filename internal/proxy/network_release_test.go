package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/testutil"
)

// A proxy that ran networked and is then told it is not waits, on Stop,
// for the addresses the running config listened on: the wildcard, not
// the loopback addresses its options now name. On macOS a bind to
// 127.0.0.1 can succeed beside a wildcard holder, which would end the
// wait while the old listener still held the port.
func TestStopWaitsWhereTheRunningConfigListened(t *testing.T) {
	var waited [][]listenPort
	orig := releasePorts
	releasePorts = func(ports []listenPort, timeout time.Duration) error {
		waited = append(waited, ports)
		return orig(ports, timeout)
	}
	t.Cleanup(func() { releasePorts = orig })
	SetSink(&recorder{})
	t.Cleanup(func() { SetSink(nil) })
	https, http, web := freePort(t), freePort(t), freePort(t)
	p := New(Options{
		StorageRoot: testutil.TempDir(t, "nh-caddy-"), HTTPSPort: https, HTTPPort: http, WebPort: web, HomeWithNoSites: true,
		Network: &NetworkOptions{Domain: netDomain, Certificates: "ca", Allow: config.DefaultNetworkAllow, Listen: true},
	})
	if err := p.Apply(nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { p.Stop() })
	p.SetNetwork(nil)
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []listenPort{
		{https, netbind.NetworkAddrs(https)},
		{http, netbind.NetworkAddrs(http)},
		{web, netbind.NetworkAddrs(web)},
	}
	if len(waited) != 1 || !reflect.DeepEqual(waited[0], want) {
		t.Errorf("Stop waited for %v, want %v", waited, want)
	}
}

// The key of the site cookies is derived from the page secret, so it is
// stable across runs and changes with the secret.
func TestSiteCookieKey(t *testing.T) {
	secret := freshSecret(t)
	key, err := SiteCookieKey()
	if err != nil {
		t.Fatalf("SiteCookieKey: %v", err)
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("nearhost site cookie"))
	if want := m.Sum(nil); !hmac.Equal(key, want) || len(key) != 32 {
		t.Errorf("SiteCookieKey = %x, want %x", key, want)
	}
}

// A running proxy can be taken onto the network and off it again: a port
// whose addresses move between the wildcard and loopback is bound afresh,
// which a socket the old config still held on the other address would
// otherwise refuse. The page's port follows each move.
func TestNetworkToggleMovesThePagePort(t *testing.T) {
	SetSink(&recorder{})
	t.Cleanup(func() { SetSink(nil) })
	https, http, web := freePort(t), freePort(t), freePort(t)
	p := New(Options{StorageRoot: testutil.TempDir(t, "nh-caddy-"), HTTPSPort: https, HTTPPort: http, WebPort: web, HomeWithNoSites: true})
	if err := p.Apply(nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { p.Stop() })
	on := &NetworkOptions{Domain: netDomain, Certificates: "ca", Allow: config.DefaultNetworkAllow, Listen: true}
	for i, n := range []*NetworkOptions{on, nil, on} {
		p.SetNetwork(n)
		if err := p.Apply(nil); err != nil {
			t.Fatalf("Apply %d (network %v): %v", i, n != nil, err)
		}
		want := portsFor(p.options())
		caddyMu.Lock()
		got := runningPorts
		caddyMu.Unlock()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Apply %d: running ports %v, want %v", i, got, want)
		}
	}
}

// failNextLoads makes the next n loads of a config fail.
func failNextLoads(t *testing.T, n int) {
	t.Helper()
	orig := loadCaddy
	left := n
	loadCaddy = func(cfg []byte, force bool) error {
		if left > 0 {
			left--
			return errors.New("injected load failure")
		}
		return orig(cfg, force)
	}
	t.Cleanup(func() { loadCaddy = orig })
}

// A config that fails to load after the proxy stopped to move its ports
// leaves the previous config serving again, and says so; the next apply
// tries again. If the previous one fails too, the error says the proxy
// serves nothing.
func TestFailedMoveRestoresThePreviousConfig(t *testing.T) {
	SetSink(&recorder{})
	t.Cleanup(func() { SetSink(nil) })
	https, http, web := freePort(t), freePort(t), freePort(t)
	p := New(Options{StorageRoot: testutil.TempDir(t, "nh-caddy-"), HTTPSPort: https, HTTPPort: http, WebPort: web, HomeWithNoSites: true})
	if err := p.Apply(nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { p.Stop() })
	loopback := portsFor(p.options())
	page := net.JoinHostPort("127.0.0.1", fmt.Sprint(web))

	p.SetNetwork(&NetworkOptions{Domain: netDomain, Certificates: "ca", Allow: config.DefaultNetworkAllow, Listen: true})
	failNextLoads(t, 1)
	err := p.Apply(nil)
	if err == nil || errors.Is(err, ErrNotServing) || !strings.Contains(err.Error(), "the previous config serves again") {
		t.Fatalf("Apply with the load failing = %v", err)
	}
	caddyMu.Lock()
	got, own := runningPorts, owner
	caddyMu.Unlock()
	if !reflect.DeepEqual(got, loopback) || own != p {
		t.Errorf("after the failed move: running ports %v (owner ours: %v), want %v", got, own == p, loopback)
	}
	if c, err := net.DialTimeout("tcp", page, time.Second); err != nil {
		t.Errorf("the page does not answer on loopback after the failed move: %v", err)
	} else {
		c.Close()
	}
	if err := p.Apply(nil); err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	caddyMu.Lock()
	got = runningPorts
	caddyMu.Unlock()
	if want := portsFor(p.options()); !reflect.DeepEqual(got, want) {
		t.Errorf("after the retry: running ports %v, want %v", got, want)
	}

	p.SetNetwork(nil)
	failNextLoads(t, 2)
	if err := p.Apply(nil); !errors.Is(err, ErrNotServing) {
		t.Errorf("Apply with both loads failing = %v, want ErrNotServing", err)
	}
	if p.Running() {
		t.Error("the proxy says it runs a config after both loads failed")
	}
	if err := p.Apply(nil); err != nil {
		t.Fatalf("Apply after nothing served: %v", err)
	}
	if !p.Running() {
		t.Error("the proxy did not start again")
	}
}
