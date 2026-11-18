package hosts

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// Lookup finds the addresses DNS gives a name. An empty answer with no error
// means the name has no address records.
type Lookup func(ctx context.Context, name string) ([]netip.Addr, error)

// lookupTimeout bounds one DNSLookup, so an unreachable nameserver delays
// `nearhost up` by seconds, not minutes.
const lookupTimeout = 2 * time.Second

// dnsPort replaces the nameserver port (53) when set. Neither resolv.conf nor
// the adapter table can name a port, so tests use it to reach a socket of
// their own.
var dnsPort string

// allowLoopbackNameserver lets tests point the nameserver list at 127.0.0.1,
// which DNSLookup otherwise refuses.
var allowLoopbackNameserver bool

// ErrNoUpstream is what DNSLookup returns when the system's nameserver runs
// on this machine and no nameserver beyond it can be found, so DNS cannot be
// asked without the hosts file answering.
var ErrNoUpstream = errors.New("the nameserver is on this machine and no upstream nameserver is listed, so DNS could not be asked without the hosts file answering")

// nameservers is the nameserver list DNSLookup asks. When the system's first
// nameserver is on this machine, as with systemd-resolved's stub at
// 127.0.0.53, it would answer from the hosts file, so nameservers turns to
// the upstream servers that stub forwards to. It returns ErrNoUpstream when
// none is listed or the first is on this machine too.
func nameservers() (*dns.ClientConfig, error) {
	conf, err := clientConfig()
	if err != nil {
		return nil, err
	}
	if len(conf.Servers) == 0 {
		return nil, errors.New(noNameserverMessage)
	}
	if !isLoopback(conf.Servers[0]) || allowLoopbackNameserver {
		return conf, nil
	}
	up, err := upstreamConfig()
	if err != nil || len(up.Servers) == 0 || isLoopback(up.Servers[0]) {
		return nil, ErrNoUpstream
	}
	return up, nil
}

// isLoopback reports whether server is a loopback address.
func isLoopback(server string) bool {
	ip, err := netip.ParseAddr(server)
	return err == nil && ip.IsLoopback()
}

// DNSLookup returns the A and AAAA records of name from the nameservers in the
// system's network configuration: /etc/resolv.conf on Unix, the network
// adapters on Windows. It asks them directly rather than through the system
// resolver, because that would read the hosts file and so report the very
// entries nearhost wrote as if DNS had supplied them. A nameserver on this
// machine, such as systemd-resolved at 127.0.0.53, answers from the hosts file
// too, so DNSLookup asks the servers it forwards to instead, and returns
// ErrNoUpstream when it finds none; Wanted then keeps the name.
func DNSLookup(ctx context.Context, name string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	conf, err := nameservers()
	if err != nil {
		return nil, err
	}
	port := conf.Port
	if dnsPort != "" {
		port = dnsPort
	}
	// The servers are asked in order, and the first that answers decides,
	// as the system resolver does. One that cannot be reached gets a share
	// of the time left, so a dead first server leaves time for the rest.
	servers := askable(conf.Servers)
	var lastErr error
	for i, s := range servers {
		sctx, scancel := context.WithTimeout(ctx, serverShare(ctx, len(servers)-i))
		addrs, err := lookupAt(sctx, net.JoinHostPort(s, port), name)
		scancel()
		var answered *rcodeError
		if err == nil || errors.As(err, &answered) || ctx.Err() != nil {
			return addrs, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// minServerShare is the least time one nameserver gets to answer, however
// many are left to ask.
const minServerShare = 500 * time.Millisecond

// serverShare is the time one of left nameservers gets: an equal share of
// what remains of ctx, and at least minServerShare.
func serverShare(ctx context.Context, left int) time.Duration {
	share := lookupTimeout / time.Duration(left)
	if deadline, ok := ctx.Deadline(); ok {
		share = time.Until(deadline) / time.Duration(left)
	}
	return max(share, minServerShare)
}

// askable is servers without the loopback ones after the first, which may
// answer from the hosts file. nameservers has already vouched for the first.
func askable(servers []string) []string {
	out := servers[:1:1]
	for _, s := range servers[1:] {
		if !isLoopback(s) {
			out = append(out, s)
		}
	}
	return out
}

// lookupAt asks server for the A and AAAA records of name at once. When one
// query cannot reach the server, its error wins over an answer's, so that
// DNSLookup moves on to the next server.
func lookupAt(ctx context.Context, server, name string) ([]netip.Addr, error) {
	var (
		mu    sync.Mutex
		addrs []netip.Addr
		errs  []error
		wg    sync.WaitGroup
	)
	for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := query(ctx, server, name, qtype)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			addrs = append(addrs, got...)
		}()
	}
	wg.Wait()
	if len(errs) == 0 {
		return addrs, nil
	}
	for _, err := range errs {
		var answered *rcodeError
		if !errors.As(err, &answered) {
			return nil, err
		}
	}
	return nil, errs[0]
}

// rcodeError is a nameserver's answer that it could not resolve the name,
// such as SERVFAIL or REFUSED. It is an answer, so DNSLookup asks no other
// server.
type rcodeError struct {
	rcode  int
	server string
}

// Error names the response code and the server that sent it.
func (e *rcodeError) Error() string {
	return dns.RcodeToString[e.rcode] + " from " + e.server
}

// exchange sends msg to server over network, udp or tcp, and returns the
// reply. Tests replace it.
var exchange = func(ctx context.Context, msg *dns.Msg, server, network string) (*dns.Msg, error) {
	c := &dns.Client{Net: network, Timeout: lookupTimeout}
	resp, _, err := c.ExchangeContext(ctx, msg, server)
	return resp, err
}

// query asks server for the records of one type, retrying over TCP when the
// UDP answer was truncated.
func query(ctx context.Context, server, name string, qtype uint16) ([]netip.Addr, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), qtype)
	msg.RecursionDesired = true
	resp, err := exchange(ctx, msg, server, "udp")
	if err == nil && resp.Truncated {
		resp, err = exchange(ctx, msg, server, "tcp")
	}
	if err != nil {
		return nil, err
	}
	// NXDOMAIN is an answer: the name has no addresses.
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		return nil, &rcodeError{rcode: resp.Rcode, server: server}
	}
	var out []netip.Addr
	for _, rr := range resp.Answer {
		var ip net.IP
		switch r := rr.(type) {
		case *dns.A:
			ip = r.A
		case *dns.AAAA:
			ip = r.AAAA
		}
		if a, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, a.Unmap())
		}
	}
	return out, nil
}

// CachedLookup wraps next so each name is looked up at most once per ttl,
// or once per failTTL while lookups of it fail. The interface checks the
// hosts file every second, and asking DNS that often for every host would
// cost a round trip per name per tick; a minute-old answer is fresh enough,
// as DNS changes for these names are rare and a stale one only costs a
// redundant or late hosts entry. Failures expire sooner so a nameserver that
// comes back is noticed. A lookup cut short by its caller's context says
// nothing about the name and is not kept. It is safe for concurrent use;
// two callers missing the same name at once may both ask next.
func CachedLookup(next Lookup, ttl, failTTL time.Duration) Lookup {
	return cachedLookup(next, ttl, failTTL, time.Now)
}

// cachedAnswer is one name's answer and when it stops being used.
type cachedAnswer struct {
	addrs   []netip.Addr
	err     error
	expires time.Time
}

// cachedLookup is CachedLookup with a clock tests can move.
func cachedLookup(next Lookup, ttl, failTTL time.Duration, now func() time.Time) Lookup {
	var (
		mu    sync.Mutex
		cache = map[string]cachedAnswer{}
	)
	return func(ctx context.Context, name string) ([]netip.Addr, error) {
		mu.Lock()
		a, ok := cache[name]
		mu.Unlock()
		if ok && now().Before(a.expires) {
			return slices.Clone(a.addrs), a.err
		}
		addrs, err := next(ctx, name)
		if ctx.Err() != nil {
			return addrs, err
		}
		keep := ttl
		if err != nil {
			keep = failTTL
		}
		mu.Lock()
		cache[name] = cachedAnswer{addrs: slices.Clone(addrs), err: err, expires: now().Add(keep)}
		mu.Unlock()
		return addrs, err
	}
}

// Wanted returns, sorted, the names the hosts file has to hold. Every .test
// name stays, as no DNS serves it. Any other name stays unless lookup gives
// addresses that are all loopback, which means DNS already sends it to this
// machine. A name stays when lookup fails, since an entry that turns out
// redundant is harmless and a missing one breaks the project. Lookups run
// concurrently. A nil lookup keeps every name.
func Wanted(ctx context.Context, names []string, lookup Lookup) []string {
	keep := make([]bool, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		if lookup == nil || strings.HasSuffix(n, ".test") {
			keep[i] = true
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs, err := lookup(ctx, n)
			keep[i] = err != nil || len(addrs) == 0 || !allLoopback(addrs)
		}()
	}
	wg.Wait()
	var out []string
	for i, n := range names {
		if keep[i] {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func allLoopback(addrs []netip.Addr) bool {
	for _, a := range addrs {
		if !a.IsLoopback() {
			return false
		}
	}
	return true
}
