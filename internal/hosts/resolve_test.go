package hosts

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestWantedSkipsNamesThatResolveToLoopback(t *testing.T) {
	lookup := func(_ context.Context, name string) ([]netip.Addr, error) {
		switch name {
		case "app.local.example.io":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "mixed.example.io":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("93.184.216.34")}, nil
		}
		return nil, nil // NXDOMAIN
	}
	got := Wanted(context.Background(), []string{"x.test", "app.local.example.io", "mixed.example.io", "new.example.io"}, lookup)
	want := []string{"mixed.example.io", "new.example.io", "x.test"}
	if !slices.Equal(got, want) {
		t.Fatalf("Wanted = %v, want %v", got, want)
	}
}

func TestWantedKeepsNamesWhenLookupFails(t *testing.T) {
	fail := func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no network") }
	if got := Wanted(context.Background(), []string{"app.local.example.io"}, fail); !slices.Equal(got, []string{"app.local.example.io"}) {
		t.Fatalf("Wanted = %v", got)
	}
}

func TestWantedWithoutLookupKeepsEverything(t *testing.T) {
	got := Wanted(context.Background(), []string{"b.example.io", "a.test"}, nil)
	if !slices.Equal(got, []string{"a.test", "b.example.io"}) {
		t.Fatalf("Wanted = %v", got)
	}
}

func TestDNSLookupTimesOut(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	// The nameserver reads queries and never answers.
	go func() {
		buf := make([]byte, 512)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	host, port, _ := net.SplitHostPort(pc.LocalAddr().String())
	// The nameserver list cannot name a port, so dnsPort points the client at ours.
	useNameservers(t, host)
	oldPort := dnsPort
	dnsPort = port
	defer func() { dnsPort = oldPort }()
	allowLoopbackNameserver = true
	defer func() { allowLoopbackNameserver = false }()

	start := time.Now()
	_, err = DNSLookup(context.Background(), "app.local.example.io")
	if err == nil {
		t.Fatal("DNSLookup succeeded against a nameserver that never answers")
	}
	if elapsed := time.Since(start); elapsed > lookupTimeout+time.Second {
		t.Fatalf("DNSLookup took %v", elapsed)
	}
}

// countingLookup answers every name with loopback, or with err when set,
// and counts the calls per name.
type countingLookup struct {
	mu    sync.Mutex
	calls map[string]int
	err   error
}

func (c *countingLookup) lookup(_ context.Context, name string) ([]netip.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[name]++
	if c.err != nil {
		return nil, c.err
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func (c *countingLookup) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func TestCachedLookupReusesAnswersWithinTheTTL(t *testing.T) {
	next := &countingLookup{}
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	lookup := cachedLookup(next.lookup, time.Minute, 10*time.Second, func() time.Time { return clock })
	ctx := context.Background()
	for range 3 {
		addrs, err := lookup(ctx, "app.local.example.io")
		if err != nil || len(addrs) != 1 || !addrs[0].IsLoopback() {
			t.Fatalf("lookup = %v, %v", addrs, err)
		}
	}
	if _, err := lookup(ctx, "other.example.io"); err != nil {
		t.Fatal(err)
	}
	if n := next.count("app.local.example.io"); n != 1 {
		t.Errorf("looked up %d times within the ttl, want 1", n)
	}
	if n := next.count("other.example.io"); n != 1 {
		t.Errorf("another name looked up %d times, want 1 of its own", n)
	}
	clock = clock.Add(time.Minute + time.Second)
	if _, err := lookup(ctx, "app.local.example.io"); err != nil {
		t.Fatal(err)
	}
	if n := next.count("app.local.example.io"); n != 2 {
		t.Errorf("looked up %d times after the ttl, want 2", n)
	}
}

func TestCachedLookupRetriesAFailureAfterFailTTL(t *testing.T) {
	next := &countingLookup{err: errors.New("i/o timeout")}
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	lookup := cachedLookup(next.lookup, time.Minute, 10*time.Second, func() time.Time { return clock })
	ctx := context.Background()
	for range 2 {
		if _, err := lookup(ctx, "app.local.example.io"); err == nil {
			t.Fatal("the failure was not passed on")
		}
	}
	if n := next.count("app.local.example.io"); n != 1 {
		t.Errorf("looked up %d times within failTTL, want 1", n)
	}
	next.mu.Lock()
	next.err = nil
	next.mu.Unlock()
	clock = clock.Add(11 * time.Second)
	if addrs, err := lookup(ctx, "app.local.example.io"); err != nil || len(addrs) != 1 {
		t.Fatalf("after failTTL: %v, %v", addrs, err)
	}
	if n := next.count("app.local.example.io"); n != 2 {
		t.Errorf("looked up %d times after failTTL, want 2", n)
	}
}

// A lookup cut short by its caller's context is not an answer about the
// name, so the next caller asks again.
func TestCachedLookupDoesNotKeepACancelledLookup(t *testing.T) {
	next := &countingLookup{err: context.Canceled}
	lookup := CachedLookup(next.lookup, time.Minute, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = lookup(ctx, "app.local.example.io")
	next.mu.Lock()
	next.err = nil
	next.mu.Unlock()
	if _, err := lookup(context.Background(), "app.local.example.io"); err != nil {
		t.Fatalf("the cancelled lookup was kept: %v", err)
	}
}

// Run with -race: callers share one cache.
func TestCachedLookupIsSafeForConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	next := func(_ context.Context, name string) ([]netip.Addr, error) {
		calls.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	lookup := CachedLookup(next, time.Minute, 10*time.Second)
	names := []string{"a.example.io", "b.example.io", "c.example.io"}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs, err := lookup(context.Background(), names[i%len(names)])
			if err != nil || len(addrs) != 1 {
				t.Errorf("lookup = %v, %v", addrs, err)
				return
			}
			addrs[0] = netip.Addr{} // a caller's copy is its own
		}()
	}
	wg.Wait()
	for _, n := range names {
		addrs, _ := lookup(context.Background(), n)
		if !addrs[0].IsLoopback() {
			t.Errorf("%s: a caller changed the cached answer: %v", n, addrs)
		}
	}
	if c := calls.Load(); c < 3 || c > 50 {
		t.Errorf("%d lookups", c)
	}
}

func TestDNSLookupRefusesLoopbackNameserver(t *testing.T) {
	for _, server := range []string{"127.0.0.53", "::1"} {
		useNameservers(t, server)
		_, err := DNSLookup(context.Background(), "app.example.io")
		if !errors.Is(err, ErrNoUpstream) {
			t.Errorf("nameserver %s: err = %v, want ErrNoUpstream", server, err)
		}
		got := Wanted(context.Background(), []string{"app.example.io"}, DNSLookup)
		if !slices.Equal(got, []string{"app.example.io"}) {
			t.Errorf("nameserver %s: Wanted = %v, want app.example.io kept", server, got)
		}
	}
}

// useNameservers makes clientConfig list servers, and upstreamConfig list
// none, until the test ends, so no test reads this machine's resolver files.
func useNameservers(t *testing.T, servers ...string) {
	t.Helper()
	old, oldUpstream := clientConfig, upstreamConfig
	clientConfig = func() (*dns.ClientConfig, error) {
		return &dns.ClientConfig{Servers: servers, Port: "53", Timeout: 5, Attempts: 2}, nil
	}
	upstreamConfig = func() (*dns.ClientConfig, error) {
		return &dns.ClientConfig{Port: "53", Timeout: 5, Attempts: 2}, nil
	}
	t.Cleanup(func() { clientConfig, upstreamConfig = old, oldUpstream })
}

func TestNameserversFromSkipsPlaceholdersAndDuplicates(t *testing.T) {
	in := []netip.Addr{
		netip.MustParseAddr("192.168.77.1"),
		netip.MustParseAddr("fec0:0:0:ffff::1"),
		netip.MustParseAddr("192.168.77.1"),
		netip.MustParseAddr("::"),
		netip.MustParseAddr("fe80::1"),
		netip.MustParseAddr("2001:db8::53"),
	}
	want := []string{"192.168.77.1", "2001:db8::53"}
	if got := nameserversFrom(in); !slices.Equal(got, want) {
		t.Fatalf("nameserversFrom = %q, want %q", got, want)
	}
}

func TestDNSLookupSaysWhenNoAdapterHasANameserver(t *testing.T) {
	useNameservers(t)
	_, err := DNSLookup(context.Background(), "app.example.io")
	if err == nil || err.Error() != noNameserverMessage {
		t.Fatalf("err = %v, want %q", err, noNameserverMessage)
	}
	want := "no nameserver in /etc/resolv.conf"
	if runtime.GOOS == "windows" {
		want = "no nameserver on any network adapter"
	}
	if noNameserverMessage != want {
		t.Fatalf("noNameserverMessage = %q, want %q", noNameserverMessage, want)
	}
}

// errRefused is the error of a fake nameserver that refuses the connection.
var errRefused = errors.New("connect: connection refused")

// fakeServer is how a fake nameserver answers: with err, by hanging until
// the query's deadline, or with rcode and, for an A query, the address a.
type fakeServer struct {
	err   error
	hang  bool
	rcode int
	a     string
}

// useExchange makes DNSLookup's queries go to servers, keyed by host:port,
// until the test ends. asked lists the servers queried, once each, in order.
func useExchange(t *testing.T, servers map[string]fakeServer) (asked func() []string) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []string
	)
	old := exchange
	exchange = func(ctx context.Context, msg *dns.Msg, server, _ string) (*dns.Msg, error) {
		mu.Lock()
		if !slices.Contains(got, server) {
			got = append(got, server)
		}
		mu.Unlock()
		s, ok := servers[server]
		switch {
		case !ok:
			t.Errorf("asked %s, which the test does not list", server)
			return nil, errRefused
		case s.hang:
			<-ctx.Done()
			return nil, ctx.Err()
		case s.err != nil:
			return nil, s.err
		}
		resp := new(dns.Msg)
		resp.SetRcode(msg, s.rcode)
		if s.a != "" && msg.Question[0].Qtype == dns.TypeA {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: msg.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET},
				A:   net.ParseIP(s.a),
			})
		}
		return resp, nil
	}
	t.Cleanup(func() { exchange = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

func TestDNSLookupMovesToTheNextServerWhenOneRefuses(t *testing.T) {
	useNameservers(t, "192.0.2.1", "192.0.2.2")
	asked := useExchange(t, map[string]fakeServer{
		"192.0.2.1:53": {err: errRefused},
		"192.0.2.2:53": {a: "93.184.216.34"},
	})
	addrs, err := DNSLookup(context.Background(), "app.example.io")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "93.184.216.34" {
		t.Fatalf("DNSLookup = %v, %v", addrs, err)
	}
	if !slices.Equal(asked(), []string{"192.0.2.1:53", "192.0.2.2:53"}) {
		t.Errorf("asked %q", asked())
	}
}

func TestDNSLookupGivesTheLastErrorWhenEveryServerFails(t *testing.T) {
	last := errors.New("connect: network is unreachable")
	useNameservers(t, "192.0.2.1", "2001:db8::53")
	useExchange(t, map[string]fakeServer{
		"192.0.2.1:53":      {err: errRefused},
		"[2001:db8::53]:53": {err: last},
	})
	if _, err := DNSLookup(context.Background(), "app.example.io"); !errors.Is(err, last) {
		t.Fatalf("err = %v, want %v", err, last)
	}
}

// An answer ends the lookup, NXDOMAIN and SERVFAIL included: only a server
// that cannot be reached sends it to the next.
func TestDNSLookupStopsAtTheFirstAnswer(t *testing.T) {
	for _, rcode := range []int{dns.RcodeNameError, dns.RcodeServerFailure} {
		useNameservers(t, "192.0.2.1", "192.0.2.2")
		asked := useExchange(t, map[string]fakeServer{
			"192.0.2.1:53": {rcode: rcode},
			"192.0.2.2:53": {a: "93.184.216.34"},
		})
		addrs, err := DNSLookup(context.Background(), "app.example.io")
		if len(addrs) != 0 || (err == nil) != (rcode == dns.RcodeNameError) {
			t.Errorf("%s: DNSLookup = %v, %v", dns.RcodeToString[rcode], addrs, err)
		}
		if !slices.Equal(asked(), []string{"192.0.2.1:53"}) {
			t.Errorf("%s: asked %q", dns.RcodeToString[rcode], asked())
		}
	}
}

// A server that never answers gets its share of lookupTimeout, not all of
// it, so the next one is still asked.
func TestDNSLookupLeavesTimeForTheNextServer(t *testing.T) {
	useNameservers(t, "192.0.2.1", "192.0.2.2", "192.0.2.3")
	asked := useExchange(t, map[string]fakeServer{
		"192.0.2.1:53": {hang: true},
		"192.0.2.2:53": {hang: true},
		"192.0.2.3:53": {a: "93.184.216.34"},
	})
	start := time.Now()
	addrs, err := DNSLookup(context.Background(), "app.example.io")
	if err != nil || len(addrs) != 1 {
		t.Fatalf("DNSLookup = %v, %v", addrs, err)
	}
	if elapsed := time.Since(start); elapsed > lookupTimeout {
		t.Errorf("DNSLookup took %v", elapsed)
	}
	if len(asked()) != 3 {
		t.Errorf("asked %q", asked())
	}
}

// Loopback servers after the first are skipped: they may answer from the
// hosts file.
func TestDNSLookupSkipsLoopbackServers(t *testing.T) {
	useNameservers(t, "192.0.2.1", "127.0.0.1", "192.0.2.2")
	asked := useExchange(t, map[string]fakeServer{
		"192.0.2.1:53": {err: errRefused},
		"192.0.2.2:53": {a: "93.184.216.34"},
	})
	if _, err := DNSLookup(context.Background(), "app.example.io"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked(), []string{"192.0.2.1:53", "192.0.2.2:53"}) {
		t.Errorf("asked %q", asked())
	}
}
