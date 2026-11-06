package proxy

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
)

// Listening on loopback only keeps Windows from asking the user to let
// Nearhost through the firewall.
func TestServersListenOnLoopbackOnWindows(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}},
		Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, WebPort: 6327})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string `json:"listen"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	servers := cfg.Apps.HTTP.Servers
	if len(servers) != 3 {
		t.Fatalf("servers = %v, want the HTTPS, HTTP and web servers", servers)
	}
	ports := map[string]int{"nearhost": 8443, "nearhost_http": 8080, "nearhost_web": 6327}
	for name, s := range servers {
		port, ok := ports[name]
		if !ok {
			t.Errorf("unexpected server %s", name)
			continue
		}
		if want := netbind.ServeAddrs(port); !slices.Equal(s.Listen, want) {
			t.Errorf("server %s listens on %q, want %q", name, s.Listen, want)
		}
		if !slices.Contains(s.Listen, fmt.Sprintf("127.0.0.1:%d", port)) {
			t.Errorf("server %s listens on %q, without 127.0.0.1", name, s.Listen)
		}
	}
}
