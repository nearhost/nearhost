package proxy

import (
	"encoding/json"
	"slices"
	"testing"
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
	for name, s := range servers {
		for _, addr := range s.Listen {
			if !slices.Contains([]string{"127.0.0.1", "[::1]"}, addr[:len(addr)-5]) {
				t.Errorf("server %s listens on %s, want 127.0.0.1 or [::1]", name, addr)
			}
		}
		if len(s.Listen) != 2 {
			t.Errorf("server %s listens on %q, want 127.0.0.1 and [::1]", name, s.Listen)
		}
	}
}
