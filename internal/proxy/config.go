// Package proxy embeds Caddy to serve projects at https://<alias>.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CAName is the display name of the local certificate authority.
const CAName = "Nearhost Local CA"

// writerModule is the Caddy log writer module name registered in logwriter.go.
const writerModule = "nearhost"

// loopbackRanges are the client addresses sites are served to. The proxy
// listens on every interface (a non-root process on macOS can bind a low port
// only on the wildcard address) while every alias resolves to loopback, so no
// legitimate client comes from anywhere else. Caddy compares the remote
// address as written and does not unmap, so the IPv4-mapped form of the
// loopback network is listed too.
var loopbackRanges = []string{"127.0.0.0/8", "::1", "::ffff:127.0.0.0/104"}

// Site is one alias proxied to one upstream.
type Site struct {
	Alias    string // e.g. myapp.test
	Upstream string // host:port, e.g. 127.0.0.1:54321
}

// Options configures the embedded Caddy instance.
type Options struct {
	StorageRoot string // Caddy storage directory (holds the local CA)
	HTTPSPort   int    // 0 means 443
	HTTPPort    int    // 0 means 80
}

func writer(alias string) map[string]any {
	w := map[string]any{"output": writerModule}
	if alias != "" {
		w["alias"] = alias
	}
	return w
}

// BuildConfig renders the complete Caddy JSON (admin, storage, logging, PKI,
// TLS automation and HTTP server) for the given sites. It is the single place
// the config shape is defined; call it with no sites to get just the base
// config, which declares no listeners. Output is deterministic: sites are ordered by alias.
func BuildConfig(sites []Site, opts Options) ([]byte, error) {
	if opts.StorageRoot == "" {
		return nil, errors.New("proxy: storage root is required")
	}
	httpsPort, httpPort := opts.HTTPSPort, opts.HTTPPort
	if httpsPort == 0 {
		httpsPort = 443
	}
	if httpPort == 0 {
		httpPort = 80
	}

	sites = append([]Site(nil), sites...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].Alias < sites[j].Alias })

	routes := make([]any, 0, len(sites))
	loggerNames := map[string]any{}
	logs := map[string]any{
		"default": map[string]any{
			"writer":  writer(""),
			"level":   "WARN",
			"exclude": []string{"http.log.access"},
		},
	}
	subjects := make([]string, 0, len(sites))
	seen := map[string]bool{} // lower-cased: host names match without case
	for _, s := range sites {
		if s.Alias == "" || s.Upstream == "" {
			return nil, fmt.Errorf("proxy: site needs alias and upstream, got %+v", s)
		}
		key := strings.ToLower(s.Alias)
		if seen[key] {
			return nil, fmt.Errorf("proxy: duplicate alias %q", s.Alias)
		}
		seen[key] = true
		routes = append(routes, map[string]any{
			// One match set, so both must hold: the host is this alias and
			// the client is on this machine.
			"match": []any{map[string]any{
				"host":      []string{s.Alias},
				"remote_ip": map[string]any{"ranges": loopbackRanges},
			}},
			"handle": []any{map[string]any{
				"handler":        "reverse_proxy",
				"upstreams":      []any{map[string]any{"dial": s.Upstream}},
				"flush_interval": -1,
				// By default Caddy closes upgraded streams (WebSockets) on every config
				// reload, dropping dev-server HMR sockets whenever the site set
				// changes. With this delay, upgraded connections of a removed site, and
				// connections open when the proxy stops, stay open until either end
				// closes or the delay elapses.
				"stream_close_delay": "24h",
				"headers": map[string]any{"request": map[string]any{"set": map[string]any{
					"X-Forwarded-Proto": []string{"https"},
					"X-Forwarded-For":   []string{"{http.request.remote.host}"},
				}}},
			}},
			"terminal": true,
		})
		loggerNames[s.Alias] = []string{s.Alias}
		logs[s.Alias] = map[string]any{
			"writer":  writer(s.Alias),
			"include": []string{"http.log.access." + s.Alias},
		}
		subjects = append(subjects, s.Alias)
	}

	// Whatever matched no site (a client from another machine, or a host that
	// is not served) gets the connection dropped without an answer, which
	// tells it nothing about the aliases.
	routes = append(routes, map[string]any{
		"handle": []any{map[string]any{"handler": "static_response", "abort": true}},
	})

	// An empty-subject policy would match every name, so only add one when
	// there are sites.
	policies := []any{}
	if len(subjects) > 0 {
		policies = append(policies, map[string]any{
			"subjects": subjects,
			"issuers":  []any{map[string]any{"module": "internal"}},
		})
	}

	cfg := map[string]any{
		// persist=false stops Caddy autosaving this config over the user's own
		// Caddy install.
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"logging": map[string]any{"logs": logs},
		"storage": map[string]any{"module": "file_system", "root": opts.StorageRoot},
		"apps": map[string]any{
			"http": map[string]any{
				"https_port": httpsPort,
				"http_port":  httpPort,
				"servers": map[string]any{
					"nearhost": map[string]any{
						"listen": []string{fmt.Sprintf(":%d", httpsPort)},
						// The only policy is for loopback clients, so a
						// handshake from anywhere else matches none and
						// fails before a certificate is shown: it cannot
						// tell a served alias from any other name.
						"tls_connection_policies": []any{map[string]any{
							"match": map[string]any{"remote_ip": map[string]any{"ranges": loopbackRanges}},
						}},
						"routes": routes,
						"logs":   map[string]any{"logger_names": loggerNames},
					},
				},
			},
			"tls": map[string]any{
				// Storage cleaning calls caddy.InstanceID, which writes instance.uuid
				// into Caddy's default data dir (the user's own Caddy directory),
				// outside our storage root. Internal-issuer certs are overwritten in
				// place, so nothing accumulates without the cleaner.
				"disable_storage_clean": true,
				"automation": map[string]any{
					"policies": policies,
				},
			},
			"pki": map[string]any{
				"certificate_authorities": map[string]any{
					"local": map[string]any{
						"name":          CAName,
						"install_trust": false,
					},
				},
			},
		},
	}
	// With no sites there is nothing to serve, so leave the HTTP app out and
	// Caddy opens no listeners. The PKI app still creates the local CA root,
	// which is how EnsureCA makes the CA without binding ports 80/443.
	if len(sites) == 0 {
		delete(cfg["apps"].(map[string]any), "http")
	}
	return json.Marshal(cfg)
}
