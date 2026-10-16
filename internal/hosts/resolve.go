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

// DNSLookup returns the A and AAAA records of name from the nameservers in
// /etc/resolv.conf. It asks them directly rather than through the system
// resolver, because that would read the hosts file and so report the very
// entries nearhost wrote as if DNS had supplied them.
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
