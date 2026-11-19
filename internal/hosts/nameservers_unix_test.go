//go:build unix

package hosts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestClientConfigReadsResolvConf(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(conf, []byte("nameserver 192.0.2.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := resolvConf
	resolvConf = conf
	defer func() { resolvConf = old }()
	c, err := clientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Servers, []string{"192.0.2.53"}) {
		t.Fatalf("servers = %q", c.Servers)
	}
}

// noFile stands for a resolv.conf that does not exist.
const noFile = "<no file>"

// useResolvConfs points clientConfig at a resolv.conf holding system and
// upstreamConfig at one holding upstream, each written unless it is noFile,
// until the test ends.
func useResolvConfs(t *testing.T, system, upstream string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if content != noFile {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	oldSystem, oldUpstream := resolvConf, resolvedConf
	resolvConf = write("resolv.conf", system)
	resolvedConf = write("resolved.conf", upstream)
	t.Cleanup(func() { resolvConf, resolvedConf = oldSystem, oldUpstream })
}

// stubResolvConf is /etc/resolv.conf as systemd-resolved writes it.
const stubResolvConf = "nameserver 127.0.0.53\noptions edns0 trust-ad\nsearch lan\n"

func TestNameserversAskSystemdResolvedUpstream(t *testing.T) {
	useResolvConfs(t, stubResolvConf, "nameserver 192.0.2.1\nnameserver 2001:db8::53\nsearch lan\n")
	c, err := nameservers()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Servers, []string{"192.0.2.1", "2001:db8::53"}) {
		t.Fatalf("servers = %q", c.Servers)
	}
}

func TestNameserversWithoutUpstreamSayNoUpstream(t *testing.T) {
	for name, upstream := range map[string]string{
		"missing":          noFile,
		"0-byte":           "",
		"loopback":         "nameserver 127.0.0.53\n",
		"search line only": "search lan\n",
	} {
		useResolvConfs(t, stubResolvConf, upstream)
		if _, err := nameservers(); !errors.Is(err, ErrNoUpstream) {
			t.Errorf("%s upstream file: err = %v, want ErrNoUpstream", name, err)
		}
		if _, err := DNSLookup(context.Background(), "app.example.io"); !errors.Is(err, ErrNoUpstream) {
			t.Errorf("%s upstream file: DNSLookup err = %v, want ErrNoUpstream", name, err)
		}
	}
}

func TestNameserversKeepAPublicResolvConf(t *testing.T) {
	useResolvConfs(t, "nameserver 192.0.2.7\n", "nameserver 192.0.2.1\n")
	c, err := nameservers()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Servers, []string{"192.0.2.7"}) {
		t.Fatalf("servers = %q", c.Servers)
	}
}

// A zoned link-local server listed first is not loopback, so it is asked
// first, and the next is asked when it does not answer.
func TestNameserversKeepAZonedUpstreamFirst(t *testing.T) {
	useResolvConfs(t, stubResolvConf, "nameserver fe80::1%2\nnameserver 192.0.2.1\n")
	c, err := nameservers()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Servers, []string{"fe80::1%2", "192.0.2.1"}) {
		t.Fatalf("servers = %q", c.Servers)
	}
	asked := useExchange(t, map[string]fakeServer{
		"[fe80::1%2]:53": {err: errRefused},
		"192.0.2.1:53":   {a: "93.184.216.34"},
	})
	addrs, err := DNSLookup(context.Background(), "app.example.io")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "93.184.216.34" {
		t.Fatalf("DNSLookup = %v, %v", addrs, err)
	}
	if !slices.Equal(asked(), []string{"[fe80::1%2]:53", "192.0.2.1:53"}) {
		t.Errorf("asked %q", asked())
	}
}
