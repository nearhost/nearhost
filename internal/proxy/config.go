// Package proxy embeds Caddy to serve projects at https://<alias>.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/hostname"
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

// Site is one web route of a project: the hosts it is served at, proxied to
// one upstream. A project with several web routes is several sites sharing
// an Alias. No host may be on two sites.
type Site struct {
	// Alias is the project's .test alias, e.g. myapp.test. It names the
	// project in logs and captured exchanges, whichever host was asked for.
	Alias string
	// Hosts are the names served, exact or patterns such as
	// *.local.example.io. There must be at least one.
	Hosts    []string
	Upstream string // host:port, e.g. 127.0.0.1:54321
}

// Options configures the embedded Caddy instance.
type Options struct {
	StorageRoot string // Caddy storage directory (holds the local CA)
	HTTPSPort   int    // 0 means 443
	HTTPPort    int    // 0 means 80
	// Capture keeps each request and response in memory (see Capture), so
	// they can be shown and sent again.
	Capture bool
	// Version is the Nearhost version the home page at https://localhost
	// shows. Empty means dev.
	Version string
}

// captureHandler is the handler that keeps a site's exchanges, before the
// reverse proxy, or nothing when they are not kept.
func captureHandler(alias string, on bool) []any {
	if !on {
		return nil
	}
	return []any{map[string]any{"handler": captureModule, "alias": alias}}
}

// accessLogger is the name of the server's one access logger. Caddy logs
// to http.log.access.<name>.
const accessLogger = "nearhost"

func writer(alias string) map[string]any {
	w := map[string]any{"output": writerModule}
	if alias != "" {
		w["alias"] = alias
	}
	return w
}

// accessWriter is the writer of the access log, which files each line under
// the alias the line carries.
func accessWriter() map[string]any {
	return map[string]any{"output": writerModule, "access": true}
}

// siteRoute is the route serving one host of a site: it tags the request
// with the site's alias, keeps the exchange when capture is on, and proxies
// to the upstream.
func siteRoute(s Site, host string, capture bool) map[string]any {
	handle := []any{map[string]any{"handler": tagModule, "alias": s.Alias}}
	handle = append(handle, captureHandler(s.Alias, capture)...)
	handle = append(handle, map[string]any{
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
	})
	return map[string]any{
		// One match set, so both must hold: the host is this one and the
		// client is on this machine.
		"match": []any{map[string]any{
			"host":      []string{host},
			"remote_ip": map[string]any{"ranges": loopbackRanges},
		}},
		"handle":   handle,
		"terminal": true,
	}
}

// homeRoute is the route serving Nearhost's own page at localhost to
// loopback clients. It has no tag: the page is no project's, so its access
// lines are dropped like any other untagged line.
func homeRoute(version string) map[string]any {
	if version == "" {
		version = "dev"
	}
	return map[string]any{
		"match": []any{map[string]any{
			"host":      []string{homeHost},
			"remote_ip": map[string]any{"ranges": loopbackRanges},
		}},
		"handle":   []any{map[string]any{"handler": homeModule, "version": version}},
		"terminal": true,
	}
}

// httpServer is the server on the http port. It serves loopback clients
// asking for a host in hosts a 308 to the same URL over https, and drops
// every other connection without an answer, as the https server does.
//
// Automatic HTTPS adds routes of its own to any server on the http port: a
// redirect per exact host, with no client matcher, inserted after the last
// route with a host matcher, or first of all when no route has one, and
// then a catch-all redirect appended at the end. Unchecked, those would
// answer clients on other machines and hosts that are not served. So the
// first route drops a client that is not on loopback, whatever comes after
// it; every host, exact or pattern, has its own redirect, so there is
// always a host-matched route and Caddy's redirects land after them, where
// ours have already answered; and the last of our routes drops what is
// left, so Caddy's catch-all after it is never reached.
func httpServer(hosts []string, httpPort int) map[string]any {
	abort := []any{map[string]any{"handler": "static_response", "abort": true}}
	routes := make([]any, 0, len(hosts)+2)
	routes = append(routes, map[string]any{
		"match": []any{map[string]any{"not": []any{map[string]any{
			"remote_ip": map[string]any{"ranges": loopbackRanges},
		}}}},
		"handle": abort,
	})
	for _, h := range hosts {
		routes = append(routes, map[string]any{
			"match": []any{map[string]any{
				"host":      []string{h},
				"remote_ip": map[string]any{"ranges": loopbackRanges},
			}},
			"handle": []any{map[string]any{
				"handler":     "static_response",
				"status_code": 308,
				"headers": map[string]any{
					"Location": []string{"https://{http.request.host}{http.request.uri}"},
				},
			}},
			"terminal": true,
		})
	}
	routes = append(routes, map[string]any{"handle": abort})
	return map[string]any{
		"listen": []string{fmt.Sprintf(":%d", httpPort)},
		"routes": routes,
		// This server is plain HTTP by design. Saying so stops Caddy
		// warning on every reload that it cannot have automatic HTTPS (it
		// logs that at info instead) or HTTP/2 and HTTP/3, which need TLS.
		// Disabling automatic HTTPS here does not stop Caddy adding the
		// https server's redirects to this server.
		"automatic_https": map[string]any{"disable": true},
		"protocols":       []string{"h1"},
	}
}

// BuildConfig renders the complete Caddy JSON (admin, storage, logging, PKI,
// TLS automation and HTTP server) for the given sites. It is the single place
// the config shape is defined; call it with no sites to get just the base
// config, which declares no listeners. Output is deterministic: there is one
// route per host, ordered by hostname.Less, so exact hosts win over patterns
// and more specific patterns over less specific ones. While there are sites
// and none serves localhost, localhost is one more exact host, served
// Nearhost's own page.
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

	// A host on two sites is reported before any host is validated, so the
	// error names the clash, whichever way the host is spelled.
	type served struct {
		site Site
		host string
		home bool // the host is localhost, served the home page
	}
	var all []served
	siteOf := map[string]int{} // lower-cased host to the index of its site: names match without case
	for i, s := range sites {
		if s.Alias == "" || s.Upstream == "" || len(s.Hosts) == 0 {
			return nil, fmt.Errorf("proxy: site needs alias, hosts and upstream, got %+v", s)
		}
		for _, h := range s.Hosts {
			key := strings.ToLower(h)
			if j, ok := siteOf[key]; ok {
				if j != i {
					return nil, fmt.Errorf("proxy: host %q is on two sites", key)
				}
				return nil, fmt.Errorf("proxy: host %q is listed twice on site %s", key, s.Alias)
			}
			siteOf[key] = i
			all = append(all, served{site: s, host: h})
		}
	}
	// While the proxy serves any project, localhost has Nearhost's own page,
	// unless a project serves localhost itself: then the page is left out
	// altogether, so it never has to be decided which of the two wins.
	// With no sites, nothing is served and nothing listens, as before.
	if _, claimed := siteOf[homeHost]; len(sites) > 0 && !claimed {
		all = append(all, served{host: homeHost, home: true})
	}
	for _, sv := range all {
		if err := hostname.Valid(sv.host); err != nil {
			return nil, fmt.Errorf("proxy: %w", err)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.host != b.host {
			return hostname.Less(a.host, b.host)
		}
		return a.site.Alias < b.site.Alias
	})

	routes := make([]any, 0, len(all)+1)
	subjects := make([]string, 0, len(all))
	var patterns []string
	hosts := make([]string, 0, len(all))
	for _, sv := range all {
		if sv.home {
			routes = append(routes, homeRoute(opts.Version))
		} else {
			routes = append(routes, siteRoute(sv.site, sv.host, opts.Capture))
		}
		hosts = append(hosts, sv.host)
		if hostname.IsPattern(sv.host) {
			patterns = append(patterns, sv.host)
		} else {
			subjects = append(subjects, sv.host)
		}
	}
	logs := map[string]any{
		// Caddy's own messages, filed under no project.
		"default": map[string]any{
			"writer":  writer(""),
			"level":   "WARN",
			"exclude": []string{"http.log.access"},
		},
		// Every site's requests, filed under the alias each line carries.
		"access": map[string]any{
			"writer":  accessWriter(),
			"include": []string{"http.log.access." + accessLogger},
		},
	}

	// Whatever matched no site (a client from another machine, or a host that
	// is not served) gets the connection dropped without an answer, which
	// tells it nothing about the aliases.
	routes = append(routes, map[string]any{
		"handle": []any{map[string]any{"handler": "static_response", "abort": true}},
	})

	// An empty-subject policy would match every name, so only add one when
	// there are exact hosts.
	policies := []any{}
	if len(subjects) > 0 {
		policies = append(policies, map[string]any{
			"subjects": subjects,
			"issuers":  []any{map[string]any{"module": "internal"}},
		})
	}
	automation := map[string]any{"policies": policies}
	if len(patterns) > 0 {
		// A certificate's wildcard can only be its leftmost label, so a
		// pattern with * anywhere else, such as internal.*.local.example.io,
		// cannot be a subject. Rather than treat *.example.io one way and
		// the rest another, every pattern goes on demand: each name a
		// pattern serves is issued its own certificate at its first
		// handshake, by this catch-all policy, and the permission module
		// refuses any name that matches no pattern, so no one can have
		// certificates issued for names that are not served.
		policies = append(policies, map[string]any{
			"on_demand": true,
			"issuers":   []any{map[string]any{"module": "internal"}},
		})
		automation["policies"] = policies
		automation["on_demand"] = map[string]any{
			"permission": map[string]any{"module": permissionModule, "patterns": patterns},
		}
	}

	server := map[string]any{
		"listen": []string{fmt.Sprintf(":%d", httpsPort)},
		// The only policy is for loopback clients, so a handshake from
		// anywhere else matches none and fails before a certificate is
		// shown: it cannot tell a served alias from any other name.
		"tls_connection_policies": []any{map[string]any{
			"match": map[string]any{"remote_ip": map[string]any{"ranges": loopbackRanges}},
		}},
		"routes": routes,
		// Requests are logged under one name; the tag handler on each route
		// says which project the line is for.
		"logs": map[string]any{"default_logger_name": accessLogger},
	}
	servers := map[string]any{"nearhost": server}
	if len(patterns) > 0 {
		// Automatic HTTPS would otherwise try to manage the patterns as
		// subjects. Its redirects stay on, but on the http port our own
		// routes answer first (see httpServer).
		server["automatic_https"] = map[string]any{"skip_certificates": patterns}
	}
	servers["nearhost_http"] = httpServer(hosts, httpPort)

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
				"servers":    servers,
			},
			"tls": map[string]any{
				// Storage cleaning calls caddy.InstanceID, which writes instance.uuid
				// into Caddy's default data dir (the user's own Caddy directory),
				// outside our storage root. Internal-issuer certs are overwritten in
				// place, so nothing accumulates without the cleaner.
				"disable_storage_clean": true,
				"automation":            automation,
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
