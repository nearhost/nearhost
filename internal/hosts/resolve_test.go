package hosts

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	conf := filepath.Join(t.TempDir(), "resolv.conf")
	// resolv.conf cannot name a port, so dnsPort points the client at ours.
	if err := os.WriteFile(conf, []byte("nameserver "+host+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := resolvConf
	resolvConf = conf
	defer func() { resolvConf = old }()
	oldPort := dnsPort
	dnsPort = port
	defer func() { dnsPort = oldPort }()

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
