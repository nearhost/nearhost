package hosts

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
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
