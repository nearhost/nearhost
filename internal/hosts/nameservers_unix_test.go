//go:build unix

package hosts

import (
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
