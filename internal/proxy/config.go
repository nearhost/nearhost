// Package proxy embeds Caddy to serve projects at https://<alias>.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
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
	// Alias is the project's main name: myapp.test by default, or any
	// domain the user chose, e.g. app.local.example.io. It names the
	// project in logs and captured exchanges, whichever host was asked for.
	Alias string
	// Hosts are the names served, exact or patterns such as
	// *.local.example.io. There must be at least one.
	Hosts    []string
	Upstream string // host:port, e.g. 127.0.0.1:54321
	// Clients are extra client ranges, in CIDR form (IPv4 or IPv6), the
	// site's hosts are served to besides loopback, such as the networks
	// of local Docker containers. nil means loopback only.
	Clients []string
}

// clientRanges are the ranges for a site's extra clients: each as written
// and, for an IPv4 one, in its IPv4-mapped form, as Caddy does not unmap
// the remote address (see loopbackRanges).
func clientRanges(clients []string) ([]string, error) {
	var out []string
	for _, c := range clients {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("client range %q is not a CIDR", c)
		}
		out = append(out, c)
		if p.Addr().Is4() {
			mapped := netip.PrefixFrom(netip.AddrFrom16(p.Addr().As16()), p.Bits()+96)
			out = append(out, mapped.String())
		}
	}
	return out, nil
}

// siteRanges are the client ranges a site's hosts are served to: loopback,
// then the site's own.
func siteRanges(s Site) ([]string, error) {
	extra, err := clientRanges(s.Clients)
	if err != nil {
		return nil, fmt.Errorf("proxy: site %s: %w", s.Alias, err)
	}
	return append(slices.Clone(loopbackRanges), extra...), nil
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
	// HomeWithNoSites serves the home page, and so listens, even with no
	// sites, so https://localhost answers before the first project. Without
	// it a config with no sites declares no listeners.
	HomeWithNoSites bool
}

// listens reports whether the config for sites declares listeners: with
// any site, or with none while the home page is served without one.
func listens(sites []Site, opts Options) bool {
	return len(sites) > 0 || opts.HomeWithNoSites
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
func siteRoute(s Site, host string, ranges []string, capture bool) map[string]any {
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
		// client is on this machine or in one of the site's extra ranges.
		"match": []any{map[string]any{
			"host":      []string{host},
			"remote_ip": map[string]any{"ranges": ranges},
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

// hostRanges is a host and the client ranges it is served to. drop is
// whether a route for the host alone, dropping the connection, follows its
// own (see hostDrop).
type hostRanges struct {
	host   string
	ranges []string
	drop   bool
}

// hostDrop is the route after a host's own that drops a client the host is
// not served to. A name belongs to the site whose host is its most specific
// match, which is the first route it matches, so without this a client
// outside the site's ranges would fall through to a less specific pattern
// of a site that does let it in, and be answered for a name that is not
// that site's.
func hostDrop(host string) map[string]any {
	return map[string]any{
		"match":    []any{map[string]any{"host": []string{host}}},
		"handle":   []any{map[string]any{"handler": "static_response", "abort": true}},
		"terminal": true,
	}
}

// coversAll reports whether ranges has every range in union.
func coversAll(ranges, union []string) bool {
	for _, r := range union {
		if !slices.Contains(ranges, r) {
			return false
		}
	}
	return true
}

// httpServer is the server on the http port. It serves the clients each of
// hosts is served to (loopback, and a site's extra ranges) a 308 to the same
// URL over https, and drops every other connection without an answer, as the
// https server does. union is the union of all those ranges.
//
// Its routes are, in order: a drop for a client outside union; for each
// host, its redirect, matched by the host and the ranges it is served to,
// followed, for a host marked drop, by a drop matched by the host alone, so
// a client the host is not served to cannot fall through to a less
// specific pattern's redirect; and a drop for whatever is left.
//
// Automatic HTTPS adds routes of its own to any server on the http port: a
// redirect per exact host, with no client matcher, inserted after the last
// route with a host matcher, or first of all when no route has one, and
// then a catch-all redirect appended at the end. Unchecked, those would
// answer clients on other machines and hosts that are not served. The
// first route stops a client outside union reaching them. Every host,
// exact or pattern, has its own redirect, so there is always a
// host-matched route and Caddy's redirects land after ours; a client in
// union has by then been redirected or, by a host's drop, refused, for
// every host Caddy redirects. The last of our routes drops what is left, so
// Caddy's catch-all after it is never reached.
func httpServer(hosts []hostRanges, union []string, httpPort int) map[string]any {
	abort := []any{map[string]any{"handler": "static_response", "abort": true}}
	routes := make([]any, 0, 2*len(hosts)+2)
	routes = append(routes, map[string]any{
		"match": []any{map[string]any{"not": []any{map[string]any{
			"remote_ip": map[string]any{"ranges": union},
		}}}},
		"handle": abort,
	})
	for _, h := range hosts {
		routes = append(routes, map[string]any{
			"match": []any{map[string]any{
				"host":      []string{h.host},
				"remote_ip": map[string]any{"ranges": h.ranges},
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
		if h.drop {
			routes = append(routes, hostDrop(h.host))
		}
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
// config, which declares no listeners unless opts.HomeWithNoSites. Output is
// deterministic: there is one route per host, ordered by hostname.Less, so
// exact hosts win over patterns and more specific patterns over less
// specific ones. While any site has extra clients, a host not served to all
// of them is followed by a drop for that host alone (see hostDrop). While the config listens and no site
// serves localhost, localhost is one more exact host, served Nearhost's own
// page.
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
	rangesOf := map[string][]string{} // lower-cased host to the client ranges it is served to
	siteOf := map[string]int{}        // lower-cased host to the index of its site: names match without case
	for i, s := range sites {
		if s.Alias == "" || s.Upstream == "" || len(s.Hosts) == 0 {
			return nil, fmt.Errorf("proxy: site needs alias, hosts and upstream, got %+v", s)
		}
		ranges, err := siteRanges(s)
		if err != nil {
			return nil, err
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
			rangesOf[key] = ranges
			all = append(all, served{site: s, host: h})
		}
	}
	// While the proxy listens, localhost has Nearhost's own page, unless a
	// project serves localhost itself: then the page is left out
	// altogether, so it never has to be decided which of the two wins.
	if _, claimed := siteOf[homeHost]; listens(sites, opts) && !claimed {
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

	// While any site has extra clients, each host not served to all of them
	// is followed by a drop for that host alone, on both servers (see
	// hostDrop). With none, there is nothing to fall through to and the
	// config is as it was before sites had clients.
	union := unionRanges(sites)
	anyClients := len(union) > len(loopbackRanges)
	routes := make([]any, 0, 2*len(all)+1)
	subjects := make([]string, 0, len(all))
	var patterns []string
	hosts := make([]hostRanges, 0, len(all))
	for _, sv := range all {
		ranges := loopbackRanges
		if sv.home {
			routes = append(routes, homeRoute(opts.Version))
		} else {
			ranges = rangesOf[strings.ToLower(sv.host)]
			routes = append(routes, siteRoute(sv.site, sv.host, ranges, opts.Capture))
		}
		drop := anyClients && !coversAll(ranges, union)
		if drop {
			routes = append(routes, hostDrop(sv.host))
		}
		hosts = append(hosts, hostRanges{sv.host, ranges, drop})
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

	// Sites opted in to the same extra client ranges share one policy,
	// which matches the hosts of all of them. Groups are in the order their
	// first site appears and hosts in the order of the routes, so the
	// output is deterministic.
	type group struct {
		ranges []string
		hosts  []string
	}
	var groups []*group
	groupOf := map[string]*group{} // the site's Clients, joined, to its group
	for _, s := range sites {
		if len(s.Clients) == 0 {
			continue
		}
		key := strings.Join(s.Clients, ",")
		if groupOf[key] == nil {
			extra, _ := clientRanges(s.Clients) // already checked by siteRanges
			groupOf[key] = &group{ranges: extra}
			groups = append(groups, groupOf[key])
		}
	}
	for _, sv := range all {
		if !sv.home && len(sv.site.Clients) > 0 {
			g := groupOf[strings.Join(sv.site.Clients, ",")]
			g.hosts = append(g.hosts, sv.host)
		}
	}
	connPolicies := []any{map[string]any{
		"match": map[string]any{"remote_ip": map[string]any{"ranges": loopbackRanges}},
	}}
	for _, g := range groups {
		// Except is every host outside the group, localhost's included, so
		// a pattern of the group's does not match a name a more specific
		// host of another site owns (see HostsMatcher).
		var except []string
		for _, sv := range all {
			if !slices.Contains(g.hosts, sv.host) {
				except = append(except, sv.host)
			}
		}
		hm := map[string]any{"hosts": g.hosts}
		if len(except) > 0 {
			hm["except"] = except
		}
		connPolicies = append(connPolicies, map[string]any{"match": map[string]any{
			"remote_ip":        map[string]any{"ranges": g.ranges},
			hostsMatcherModule: hm,
		}})
	}

	server := map[string]any{
		"listen": []string{fmt.Sprintf(":%d", httpsPort)},
		// Policies are first-match. The first is for loopback clients; the
		// others, one per set of extra client ranges, are for those clients
		// and the hosts of the sites that opted in. A handshake from
		// anywhere else matches none and fails before a certificate is
		// shown: it cannot tell a served alias from any other name.
		"tls_connection_policies": connPolicies,
		"routes":                  routes,
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
	servers["nearhost_http"] = httpServer(hosts, union, httpPort)

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
	// With nothing to serve, leave the HTTP app out and Caddy opens no
	// listeners. The PKI app still creates the local CA root, which is how
	// EnsureCA makes the CA without binding ports 80/443.
	if !listens(sites, opts) {
		delete(cfg["apps"].(map[string]any), "http")
	}
	return json.Marshal(cfg)
}

// unionRanges are loopback and every site's extra client ranges, each once,
// for the http server's first route to drop the rest.
func unionRanges(sites []Site) []string {
	out := slices.Clone(loopbackRanges)
	for _, s := range sites {
		extra, _ := clientRanges(s.Clients) // already checked by siteRanges
		for _, r := range extra {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}
