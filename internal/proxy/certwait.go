package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/hostname"
)

// certWait bounds how long a load waits for the certificates of the hosts it
// adds. certPoll is the pause between two tries of one host.
var (
	certWait = 5 * time.Second
	certPoll = 5 * time.Millisecond
)

// certified holds the hosts whose certificate has answered a handshake since
// the proxy that owns Caddy took it, by the port they were asked on. Guarded
// by caddyMu.
var certified = map[string]struct{}{}

// certAsked, when set, is told the hosts a load is about to ask for. Tests
// set it to see which hosts a load waits for.
var certAsked func(hosts []string)

// awaitCertificates waits until the proxy answers a TLS handshake for each
// host of sites that has not answered one yet, for certWait at most in all.
//
// Caddy obtains a host's certificate in the background once a load has
// returned, and refuses a handshake for the host until it has it: the client
// gets "tls: internal error". The local CA signs in a few milliseconds, so
// the wait is short, but without it a project is said to run a moment before
// its address answers, and whatever opens the address at once fails.
//
// A host is asked for at the proxy's own port on the loopback address, as a
// browser on this machine asks, and the answer is checked against the local
// CA. A pattern such as *.shop.test names no single host and is left out:
// its names get their certificates at their first handshake, which waits
// for them. A host still unanswered after the wait is said in the log, once,
// and the load succeeds: the certificate may yet come.
//
// Call it with caddyMu held, after a load that succeeded.
func (p *Proxy) awaitCertificates(sites []Site) {
	want := map[string]struct{}{} // "port host"
	if listens(sites, p.opts) {
		https := p.opts.HTTPSPort
		if https == 0 {
			https = 443
		}
		for _, s := range sites {
			for _, h := range s.Hosts {
				if !hostname.IsPattern(h) {
					want[certKey(https, h)] = struct{}{}
				}
			}
		}
		if p.opts.WebPort != 0 {
			for _, h := range pageHosts() {
				want[certKey(p.opts.WebPort, h)] = struct{}{}
			}
		}
	}
	// A host the configuration no longer serves has to answer again should
	// it come back: Caddy may have let its certificate go.
	for k := range certified {
		if _, ok := want[k]; !ok {
			delete(certified, k)
		}
	}
	var ask []string
	for k := range want {
		if _, ok := certified[k]; !ok {
			ask = append(ask, k)
		}
	}
	if len(ask) == 0 {
		return
	}
	sort.Strings(ask)
	if certAsked != nil {
		hosts := make([]string, len(ask))
		for i, k := range ask {
			_, hosts[i], _ = strings.Cut(k, " ")
		}
		certAsked(hosts)
	}
	deadline := time.Now().Add(certWait)
	// The CA an answer is checked against is made by Caddy's load, or was
	// by the caller; it is waited for like the rest, should it be late.
	root, err := RootCert(p.opts.StorageRoot)
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(certPoll)
		root, err = RootCert(p.opts.StorageRoot)
	}
	pool := x509.NewCertPool()
	if err == nil {
		pool.AddCert(root)
	}
	answered := make([]bool, len(ask))
	var wg sync.WaitGroup
	for i, k := range ask {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port, host, _ := strings.Cut(k, " ")
			answered[i] = answersFor(net.JoinHostPort("127.0.0.1", port), host, pool, deadline)
		}()
	}
	wg.Wait()
	var late []string
	for i, k := range ask {
		if answered[i] {
			certified[k] = struct{}{}
			continue
		}
		_, host, _ := strings.Cut(k, " ")
		late = append(late, host)
	}
	if len(late) > 0 {
		sort.Strings(late)
		emit("", fmt.Sprintf("no certificate yet for %s after %s; HTTPS for it fails until the local CA has issued one",
			strings.Join(late, ", "), certWait))
	}
}

func certKey(port int, host string) string {
	return strconv.Itoa(port) + " " + strings.ToLower(host)
}

// answersFor reports whether addr completes a TLS handshake for host with a
// certificate that roots vouch for, trying until deadline.
func answersFor(addr, host string, roots *x509.CertPool, deadline time.Time) bool {
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: left}, "tcp", addr,
			&tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(min(certPoll, left))
	}
}
