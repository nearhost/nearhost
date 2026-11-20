package proxy

import (
	"encoding/json"
	"slices"
	"testing"
)

// serverWrappers is the listener wrapper names of each server in the config
// for one site, with the web port on.
func serverWrappers(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}},
		Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, WebPort: 6327})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Wrappers []struct {
						Wrapper string `json:"wrapper"`
					} `json:"listener_wrappers"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for name, s := range cfg.Apps.HTTP.Servers {
		out[name] = []string{}
		for _, w := range s.Wrappers {
			out[name] = append(out[name], w.Wrapper)
		}
	}
	if len(out) != 3 {
		t.Fatalf("servers = %v, want the HTTPS, HTTP and web servers", out)
	}
	return out
}

// Each server hands its connections over through Handoff, before TLS, so
// a stopping server is given none (see Handoff), and Retire, around it, sees
// each server's listener close.
func TestServersHandConnectionsToTheNewestConfig(t *testing.T) {
	for name, w := range serverWrappers(t) {
		if !slices.Equal(w, []string{handoffModule, retireModule, "tls"}) {
			t.Errorf("server %s has listener wrappers %q, want %q, %q then tls", name, w, handoffModule, retireModule)
		}
	}
}
