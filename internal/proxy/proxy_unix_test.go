//go:build unix

package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/netbind"
)

// sourceSeen dials ip and reports the address the accepting side sees the
// connection come from, or false when ip cannot be dialed or the connection
// arrives from loopback, which cannot stand in for another machine.
func sourceSeen(t *testing.T, ip net.IP) (net.IP, bool) {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		return nil, false
	}
	defer l.Close()
	c, err := net.DialTimeout("tcp", l.Addr().String(), 5*time.Second)
	if err != nil {
		return nil, false
	}
	defer c.Close()
	a, err := l.Accept()
	if err != nil {
		return nil, false
	}
	defer a.Close()
	seen := a.RemoteAddr().(*net.TCPAddr).IP
	if seen.IsLoopback() {
		return nil, false
	}
	return seen, true
}

// A site's extra client ranges are served at that site's hosts alone: a
// client in the range gets the site over https and a redirect from http,
// while a site that did not opt in stays out of reach, as for any client
// that is not on loopback.
// Unix only: Windows has no such case. There the proxy listens on loopback
// only, and Docker clients reach it from 127.0.0.1 (measured on hera,
// 2026-11-06), so no client arrives from a non-loopback address.
func TestProxyServesDockerClientsOnlyForOptedInSites(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "docker page")
	}))
	t.Cleanup(up.Close)
	other := named(t, "b page")

	tried := 0
	for _, ip := range nonLoopbackAddrs(t) {
		// The client's range is the address the proxy will see, which is
		// not always the one dialed: on a Linux host running Docker, its NAT
		// rule rewrites the source of a connection to the docker0 address.
		seen, ok := sourceSeen(t, ip)
		if !ok {
			t.Logf("%s: not dialable, or seen as loopback, skipping it", ip)
			continue
		}
		bits := 128
		if seen.To4() != nil {
			bits = 32
		}
		client := fmt.Sprintf("%s/%d", seen, bits)
		// a.test opts in and serves a pattern; b.test does not, and serves
		// a name the pattern matches, which is b.test's alone.
		h := startProxy(t, []Site{
			{Alias: "a.test", Hosts: []string{"a.test", "*.local.x.io"}, Upstream: addr(up), Clients: []string{client}},
			{Alias: "b.test", Hosts: []string{"b.test", "api.local.x.io"}, Upstream: addr(other)},
		})
		h.waitCerts(t, "a.test", "b.test", "api.local.x.io", "other.local.x.io")
		dialed, _, err := h.plainHTTP(t, ip, "a.test")
		if !dialed {
			t.Logf("%s: not dialable, skipping it: %v", ip, err)
			continue
		}
		tried++
		t.Logf("client address %s", ip)

		// get sends one GET for host over a fresh TLS connection from ip,
		// with sni as the server name.
		get := func(sni, host string) (string, error) {
			raw, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("tcp", net.JoinHostPort(ip.String(), fmt.Sprint(h.httpsPort)))
			if err != nil {
				return "", err
			}
			defer raw.Close()
			raw.SetDeadline(time.Now().Add(10 * time.Second))
			conn := tls.Client(raw, &tls.Config{RootCAs: h.pool, ServerName: sni})
			if err := conn.Handshake(); err != nil {
				return "", err
			}
			if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host); err != nil {
				return "", err
			}
			r, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				return "", err
			}
			defer r.Body.Close()
			b, _ := io.ReadAll(r.Body)
			return string(b), nil
		}
		for _, host := range []string{"a.test", "other.local.x.io"} {
			if body, err := get(host, host); err != nil || body != "docker page" {
				t.Errorf("client %s asking for %s: body %q, err %v, want the upstream's answer", ip, host, body, err)
			}
			if _, status, err := h.plainHTTP(t, ip, host); err != nil || status != 307 {
				t.Errorf("http from %s to %s: status %d, err %v, want 307", ip, host, status, err)
			}
		}
		for _, host := range []string{"b.test", "api.local.x.io"} {
			if body, err := get(host, host); err == nil {
				t.Errorf("client %s asking for %s got %q, want the handshake to fail", ip, host, body)
			}
			if _, status, err := h.plainHTTP(t, ip, host); err == nil {
				t.Errorf("http from %s to %s got status %d, want no answer", ip, host, status)
			}
			// A handshake for a.test's name does not open b.test's hosts.
			if body, err := get("a.test", host); err == nil {
				t.Errorf("client %s with SNI a.test asking for Host %s got %q, want no answer", ip, host, body)
			}
		}
		h.p.Stop()
	}
	if tried == 0 {
		t.Skip("this machine has no reachable address besides loopback, so a non-loopback client cannot be simulated")
	}
}

// Nearhost's page listens on the loopback addresses alone, while the sites'
// https and http ports keep the wildcard address that Docker clients reach.
func TestPageListensOnLoopbackOnUnix(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}},
		Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, WebPort: 6327})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"nearhost":      {sharedNetwork + "/:8443"},
		"nearhost_http": {sharedNetwork + "/:8080"},
		"nearhost_web":  {sharedNetwork + "/127.0.0.1:6327", sharedNetwork + "/[::1]:6327"},
	}
	if l, err := net.Listen("tcp", "[::1]:0"); err != nil {
		want["nearhost_web"] = want["nearhost_web"][:1] // no IPv6 loopback here
	} else {
		l.Close()
	}
	for name, addrs := range want {
		if got := strs(dig(t, cfg, "apps", "http", "servers", name, "listen")); !slices.Equal(got, addrs) {
			t.Errorf("server %s listens on %q, want %q", name, got, addrs)
		}
	}
}

// The page answers on 127.0.0.1, and a connection to another of this
// machine's addresses on its port is refused by the kernel.
func TestPageRefusesConnectionsOffLoopback(t *testing.T) {
	h := startProxyOpts(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(named(t, "project"))}}, Options{WebPort: freePort(t)})
	h.pageAnswers(t, "127.0.0.1")
	tried := 0
	for _, ip := range nonLoopbackAddrs(t) {
		// An address that cannot be listened on cannot be dialed either,
		// such as one still in IPv6 duplicate address detection.
		probe, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			continue
		}
		probe.Close()
		tried++
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(h.webPort)), 5*time.Second)
		if err == nil {
			c.Close()
			t.Errorf("a connection to %s on the page's port %d was accepted", ip, h.webPort)
			continue
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Errorf("a connection to %s on the page's port: %v, want connection refused", ip, err)
		}
	}
	if tried == 0 {
		t.Skip("this machine has no address but loopback")
	}
}

// Where the page listens on the wildcard address, as on macOS for a port
// below 1024, it answers on loopback and gives a client on another address
// nothing: the connection is accepted and dropped.
func TestWildcardPageDropsClientsOffLoopback(t *testing.T) {
	orig := pageAddrs
	pageAddrs = netbind.ServeAddrs
	t.Cleanup(func() { pageAddrs = orig })
	h := startProxyOpts(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(named(t, "project"))}}, Options{WebPort: freePort(t)})
	h.pageAnswers(t, "127.0.0.1")
	tried := 0
	for _, ip := range nonLoopbackAddrs(t) {
		if _, ok := sourceSeen(t, ip); !ok {
			continue
		}
		tried++
		tr := &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: h.pool, ServerName: homeHost},
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(h.webPort)))
			},
		}
		c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		if resp, err := c.Get(fmt.Sprintf("https://localhost:%d/", h.webPort)); err == nil {
			resp.Body.Close()
			t.Errorf("a client at %s got the page: status %d", ip, resp.StatusCode)
		}
	}
	if tried == 0 {
		t.Skip("this machine has no address but loopback that a client can come from")
	}
}

// A machine without the IPv6 loopback still serves the page, on 127.0.0.1
// alone.
func TestPageWithoutIPv6Loopback(t *testing.T) {
	orig := pageAddrs
	var asked []string
	pageAddrs = func(port int) []string {
		asked = []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
		return asked
	}
	t.Cleanup(func() { pageAddrs = orig })
	h := startProxyOpts(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(named(t, "project"))}}, Options{WebPort: freePort(t)})
	h.pageAnswers(t, "127.0.0.1")
	if asked == nil {
		t.Fatal("the config did not take the page's addresses from pageAddrs")
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("::1", strconv.Itoa(h.webPort)), 5*time.Second); err == nil {
		c.Close()
		t.Errorf("the page answered on [::1]:%d though only 127.0.0.1 was asked for", h.webPort)
	}
}

// pageAnswers fails the test unless Nearhost's page answers 200 at
// localhost on host and the harness's web port within 10 s.
func (h *harness) pageAnswers(t *testing.T, host string) {
	t.Helper()
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: h.pool, ServerName: homeHost},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, strconv.Itoa(h.webPort)))
		},
	}
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	waitUntil(t, 10*time.Second, func() bool {
		resp, err := c.Get(fmt.Sprintf("https://localhost:%d/", h.webPort))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, "Nearhost's page on "+host)
}

// Every configuration shares the one socket of an address, so a reload
// leaves the sockets as they were: none is closed with a connection queued
// on it. A stop closes them, and the ports can be bound at once.
func TestASocketOutlivesAReloadAndClosesWithTheProxy(t *testing.T) {
	up := upstream(t, "a")
	a := Site{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}
	h := startProxy(t, []Site{a})
	open := func() map[string]net.Listener {
		sockets.mu.Lock()
		defer sockets.mu.Unlock()
		out := map[string]net.Listener{}
		for addr, s := range sockets.open {
			out[addr] = s.ln
		}
		return out
	}
	before := open()
	if len(before) != 2 {
		t.Fatalf("%d sockets open, want the https and http ports'", len(before))
	}
	if err := h.p.Apply([]Site{a, {Alias: "b.test", Hosts: []string{"b.test"}, Upstream: addr(up.Server)}}); err != nil {
		t.Fatal(err)
	}
	after := open()
	if len(after) != len(before) {
		t.Fatalf("%d sockets open after a reload, want %d", len(after), len(before))
	}
	for addr, ln := range before {
		if after[addr] != ln {
			t.Errorf("the socket of %s changed in a reload", addr)
		}
	}
	if err := h.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if n := sockets.count(); n != 0 {
		t.Fatalf("%d sockets open after Stop", n)
	}
	for _, port := range []int{h.httpsPort, h.httpPort} {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			t.Fatalf("port %d not released: %v", port, err)
		}
		l.Close()
	}
}
