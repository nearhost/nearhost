//go:build unix

package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
			if _, status, err := h.plainHTTP(t, ip, host); err != nil || status != 308 {
				t.Errorf("http from %s to %s: status %d, err %v, want 308", ip, host, status, err)
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
