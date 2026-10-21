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

// resolvConf is where DNSLookup finds the nameservers. Tests replace it.
var resolvConf = "/etc/resolv.conf"

// dnsPort replaces the nameserver port (53) when set. resolv.conf cannot name
// a port, so tests use it to reach a socket of their own.
var dnsPort string

// allowLoopbackNameserver lets tests point resolv.conf at a nameserver on
// 127.0.0.1, which DNSLookup otherwise refuses.
var allowLoopbackNameserver bool

// errLocalResolver is what DNSLookup returns when the nameserver runs on this
// machine.
var errLocalResolver = errors.New("the nameserver is on this machine, so it may answer from the hosts file")

// DNSLookup returns the A and AAAA records of name from the nameservers in
// /etc/resolv.conf. It asks them directly rather than through the system
// resolver, because that would read the hosts file and so report the very
// entries nearhost wrote as if DNS had supplied them. A nameserver on this
// machine, such as systemd-resolved at 127.0.0.53, answers from the hosts file
// too, so DNSLookup refuses it with errLocalResolver and Wanted keeps the name.
func DNSLookup(ctx context.Context, name string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	conf, err := dns.ClientConfigFromFile(resolvConf)
	if err != nil {
		return nil, err
	}
	if len(conf.Servers) == 0 {
		return nil, errors.New("no nameserver in " + resolvConf)
	}
	if ip, err := netip.ParseAddr(conf.Servers[0]); err == nil && ip.IsLoopback() && !allowLoopbackNameserver {
		return nil, errLocalResolver
	}
	// The first nameserver answers; trying the rest is the system resolver's
	// job, and a wrong answer here only costs a redundant hosts entry.
	port := conf.Port
	if dnsPort != "" {
		port = dnsPort
	}
	server := net.JoinHostPort(conf.Servers[0], port)
	var (
		mu       sync.Mutex
		addrs    []netip.Addr
		firstErr error
		wg       sync.WaitGroup
	)
	for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := query(ctx, server, name, qtype)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			addrs = append(addrs, got...)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return addrs, nil
}

// query asks server for the records of one type, retrying over TCP when the
// UDP answer was truncated.
func query(ctx context.Context, server, name string, qtype uint16) ([]netip.Addr, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), qtype)
	msg.RecursionDesired = true
	c := &dns.Client{Timeout: lookupTimeout}
	resp, _, err := c.ExchangeContext(ctx, msg, server)
	if err == nil && resp.Truncated {
		c.Net = "tcp"
		resp, _, err = c.ExchangeContext(ctx, msg, server)
	}
	if err != nil {
		return nil, err
	}
	// NXDOMAIN is an answer: the name has no addresses.
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		return nil, errors.New(dns.RcodeToString[resp.Rcode] + " from " + server)
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
