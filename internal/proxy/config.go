// Package proxy embeds Caddy to serve projects at https://<alias>.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/netbind"
)

// CAName is the display name of the local certificate authority.
const CAName = "Nearhost Local CA"

// leafLifetime is how long a site's certificate lasts: a week, where
// Caddy's internal issuer defaults to 12 hours. Caddy renews a certificate
// on a timer that a sleeping computer stops, so a 12-hour one issued in the
// evening has expired when the laptop wakes. A week outlasts a night or a
// weekend, and the CA is a local one whose key is on the same disk, so a
// longer leaf gives nothing away. Tests shorten it.
var leafLifetime = 168 * time.Hour

// intermediateLifetime is how long the local CA's intermediate lasts. The
// internal issuer cuts a leaf short to what is left of its intermediate, and
// Caddy's default of 7 days would leave less than a week for most leaves.
// The root keeps Caddy's default of 10 years.
const intermediateLifetime = 720 * time.Hour

// renewInterval is how often Caddy checks its certificates for renewal,
// where its default is 10 minutes. Its timer stands still while the computer
// sleeps, so after a wake a certificate that came due, or expired, is served
// until the next check: a minute at most. A check scans the certificates in
// memory and reads storage only for one that is due. Tests shorten it.
var renewInterval = time.Minute

// caddyDuration writes d as Caddy reads a duration: 168h, 30s.
func caddyDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// internalIssuer is the issuer of every certificate policy: the local CA,
// with week-long leaves.
func internalIssuer() map[string]any {
	return map[string]any{"module": "internal", "lifetime": caddyDuration(leafLifetime)}
}

// writerModule is the Caddy log writer module name registered in logwriter.go.
const writerModule = "nearhost"

// loopbackRanges are the client addresses sites are served to. The https and
// http servers listen on netbind.ServeAddrs (every interface on Unix, as a
// non-root process on macOS can bind a low port only on the wildcard
// address) while every alias resolves to loopback, so no legitimate client
// comes from anywhere else. Nearhost's page listens on loopback alone. Caddy
// compares the remote address as written and does not unmap, so the
// IPv4-mapped form of the loopback network is listed too.
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
	Hosts []string
	// Upstream is the address requests are sent to, host:port, e.g.
	// 127.0.0.1:54321.
	Upstream string
	// Forwarded marks an Upstream that is a forward to the app's server.
	// The site then sends a request on only while SetForwardUp says the
	// forward is up, and otherwise answers 502 itself (see Gate).
	Forwarded bool
	// Clients are extra client ranges, in CIDR form (IPv4 or IPv6), the
	// site's hosts are served to besides loopback, such as the networks
	// of local Docker containers. nil means loopback only.
	Clients []string
	// Project, Port and Server name the app behind the site on the page a
	// browser gets while the app does not answer: the project's name, the
	// port the app listens on and the server it runs on, empty for this
	// machine. An empty Project means Alias, and Port 0 Upstream's port.
	Project string
	Port    int
	Server  string
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
	// Version is the Nearhost version the home page shows. Empty means dev.
	Version string
	// HomeWithNoSites listens even with no sites, so the home page on the
	// web port answers before the first project. Without it a config with
	// no sites declares no listeners.
	HomeWithNoSites bool
	// WebPort is a port of its own that serves the home page, Nearhost's
	// page, at https://nearhost.localhost:<port>, and at localhost on the
	// same port, whenever the proxy listens. It is the page's only port:
	// localhost on the https port belongs to projects. 0 means none, and
	// then the page is not served.
	WebPort int
}

// listens reports whether the config for sites declares listeners: with
// any site, or with none under opts.HomeWithNoSites.
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
// with the site's alias, keeps the exchange when opts.Capture is on, and
// proxies to the upstream, naming Nearhost in Via each way. A Forwarded
// site's gate comes just before the proxy.
func siteRoute(s Site, host string, ranges []string, opts Options) map[string]any {
	handle := []any{map[string]any{"handler": tagModule, "alias": s.Alias}}
	handle = append(handle, captureHandler(s.Alias, opts.Capture)...)
	match := []any{map[string]any{
		"host":      []string{host},
		"remote_ip": map[string]any{"ranges": ranges},
	}}
	// Caddy adds "Via: 1.1 Caddy" to the response after the proxy's own
	// header operations, so its entry is renamed here, deferred to when the
	// response is written. Hops the upstream's own proxies added stay.
	handle = append(handle, map[string]any{
		"handler":  "headers",
		"response": map[string]any{"replace": viaReplace(opts.Version), "deferred": true},
	})
	if s.Forwarded {
		project := s.Project
		if project == "" {
			project = s.Alias
		}
		gate := map[string]any{"handler": gateModule, "upstream": s.Upstream, "project": project}
		if s.Server != "" {
			gate["server"] = s.Server
		}
		handle = append(handle, gate)
	}
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
		// Request operations run after Caddy adds its own Via, so its entry
		// is renamed, keeping the hops before Nearhost.
		"headers": map[string]any{"request": map[string]any{
			"set": map[string]any{
				"X-Forwarded-Proto": []string{"https"},
				"X-Forwarded-For":   []string{"{http.request.remote.host}"},
			},
			"replace": viaReplace(opts.Version),
		}},
	})
	return map[string]any{
		// One match set, so both must hold: the host is this one and the
		// client is on this machine or in one of the site's extra ranges.
		"match":    match,
		"handle":   handle,
		"terminal": true,
	}
}

// downRoute is the error route for one host of a site: matched as its
// site route is, it serves Nearhost's page to a browser while the site's
// app does not answer (see Down). Errors are the server's, not a route's,
// so each host has its own, naming its site's app.
func downRoute(s Site, host string, ranges []string) map[string]any {
	project, port := s.Project, s.Port
	if project == "" {
		project = s.Alias
	}
	if port == 0 {
		if _, p, err := net.SplitHostPort(s.Upstream); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	handler := map[string]any{"handler": downModule, "project": project, "port": port}
	if s.Server != "" {
		handler["server"] = s.Server
	}
	return map[string]any{
		"match": []any{map[string]any{
			"host":      []string{host},
			"remote_ip": map[string]any{"ranges": ranges},
		}},
		"handle":   []any{handler},
		"terminal": true,
	}
}

// viaReplace renames the entry Caddy adds to Via, "1.1 Caddy", as
// Nearhost's: "1.1 Nearhost (0.8.0)", or "(dev)" for a build without a
// version. Caddy adds its entry as a value of its own, so the other hops'
// values are left alone, and it keeps the protocol Caddy wrote.
func viaReplace(version string) map[string]any {
	if version == "" {
		version = "dev"
	}
	return map[string]any{"Via": []any{map[string]any{
		"search_regexp": `^(\S+) Caddy$`,
		"replace":       "$1 Nearhost (" + version + ")",
	}}}
}

// homeRoute is the web server's route serving Nearhost's own page at
// pageHosts to loopback clients. It has no tag: the page is no project's.
func homeRoute(opts Options) map[string]any {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	handler := map[string]any{"handler": homeModule, "version": version, "web_port": opts.WebPort}
	return map[string]any{
		"match": []any{map[string]any{
			"host":      pageHosts(),
			"remote_ip": map[string]any{"ranges": loopbackRanges},
		}},
		"handle":   []any{handler},
		"terminal": true,
	}
}

// pageAddrs is netbind.PageAddrs; tests stand in a machine without the IPv6
// loopback, or macOS with a page port below 1024.
var pageAddrs = netbind.PageAddrs

// webServer is the server on opts.WebPort, which serves the home page at
// pageHosts to loopback clients and drops everything else. It listens on
// netbind.PageAddrs: the loopback addresses alone, so the kernel refuses a
// connection from another machine to this port before a request is read,
// except on macOS for a port below 1024 when Nearhost is not root, where it
// listens on the wildcard address. The loopback ranges below hold in both
// cases, and in the second they alone keep the page to loopback clients.
// The https server never serves the page: localhost there is a project's,
// or is not served. It has no HTTP/3, so it needs no UDP port, and no
// redirects of its own.
func webServer(opts Options) map[string]any {
	return map[string]any{
		"listen": pageAddrs(opts.WebPort),
		"tls_connection_policies": []any{map[string]any{
			"match": map[string]any{"remote_ip": map[string]any{"ranges": loopbackRanges}},
		}},
		"routes": []any{homeRoute(opts), map[string]any{
			"handle": []any{map[string]any{"handler": "static_response", "abort": true}},
		}},
		"automatic_https": map[string]any{"disable_redirects": true},
		"protocols":       []string{"h1", "h2"},
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
// hosts is served to (loopback, and a site's extra ranges) a 307 to the same
// URL over https, which carries Cache-Control: no-store so that a browser
// keeps no memory of it (a 308 is cached for good, and a host that stops
// being served or moves to another tool would stay stuck on https), and drops every other connection without an answer, as the
// https server does. The redirect names httpsPort, the effective https port,
// unless it is 443; {http.request.host} is the host without its port. union
// is the union of all those ranges.
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
// Caddy's catch-all after it is never reached. With no host at all there is
// no host-matched route to keep Caddy's redirects behind ours, so
// BuildConfig turns them off on the https server.
func httpServer(hosts []hostRanges, union []string, httpPort, httpsPort int) map[string]any {
	location := "https://{http.request.host}"
	if httpsPort != 443 {
		location += ":" + strconv.Itoa(httpsPort)
	}
	location += "{http.request.uri}"
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
				"status_code": 307,
				"headers": map[string]any{
					"Location":      []string{location},
					"Cache-Control": []string{"no-store"},
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
		"listen": netbind.ServeAddrs(httpPort),
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

// listenerWrappers sets server's listener wrappers: Handoff, before TLS,
// where configurations share their sockets, and none elsewhere.
func listenerWrappers(server map[string]any) map[string]any {
	if sharedSockets {
		server["listener_wrappers"] = []any{
			map[string]any{"wrapper": handoffModule},
			map[string]any{"wrapper": "tls"},
		}
	}
	return server
}

// BuildConfig renders the complete Caddy JSON (admin, storage, logging, PKI,
// TLS automation and HTTP server) for the given sites. It is the single place
// the config shape is defined; call it with no sites to get just the base
// config, which declares no listeners unless opts.HomeWithNoSites. Output is
// deterministic: there is one route per host, ordered by hostname.Less, so
// exact hosts win over patterns and more specific patterns over less
// specific ones. While any site has extra clients, a host not served to all
// of them is followed by a drop for that host alone (see hostDrop).
// Nearhost's own page is served on the web port alone (see webServer):
// localhost on the https port is served only to a site that lists it, and
// is otherwise dropped like any host that is not served. With a web port,
// localhost and hostname.PageHost are certificate subjects whether or not a
// site lists them. The https server's error routes, one per site host in
// the same order, give a browser Nearhost's page while that site's app does
// not answer (see Down).
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
	var downRoutes []any // the error routes, one per site host, in the order of routes
	subjects := make([]string, 0, len(all))
	var patterns []string
	hosts := make([]hostRanges, 0, len(all))
	for _, sv := range all {
		ranges := rangesOf[strings.ToLower(sv.host)]
		routes = append(routes, siteRoute(sv.site, sv.host, ranges, opts))
		downRoutes = append(downRoutes, downRoute(sv.site, sv.host, ranges))
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

	// The web port serves the home page at pageHosts, so each is a
	// certificate subject whenever that port is served, unless it already
	// is one as a project's host.
	web := opts.WebPort != 0 && listens(sites, opts)
	if web {
		for _, h := range pageHosts() {
			if _, claimed := siteOf[h]; !claimed {
				subjects = append(subjects, h)
			}
		}
	}
	// An empty-subject policy would match every name, so only add one when
	// there are exact hosts.
	policies := []any{}
	if len(subjects) > 0 {
		policies = append(policies, map[string]any{
			"subjects": subjects,
			"issuers":  []any{internalIssuer()},
		})
	}
	automation := map[string]any{"policies": policies, "renew_interval": caddyDuration(renewInterval)}
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
			"issuers":   []any{internalIssuer()},
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
		if len(sv.site.Clients) > 0 {
			g := groupOf[strings.Join(sv.site.Clients, ",")]
			g.hosts = append(g.hosts, sv.host)
		}
	}
	connPolicies := []any{map[string]any{
		"match": map[string]any{"remote_ip": map[string]any{"ranges": loopbackRanges}},
	}}
	for _, g := range groups {
		// Except is every host outside the group, so a pattern of the
		// group's does not match a name a more specific host of another
		// site owns (see HostsMatcher).
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
		"listen": netbind.ServeAddrs(httpsPort),
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
	if len(downRoutes) > 0 {
		// A site's app that does not answer fails its route, and Caddy
		// runs these: the route for the host that failed. The last route
		// catches every other error only to log it: with error routes,
		// Caddy logs the errors they handle at debug level.
		downRoutes = append(downRoutes, map[string]any{
			"handle":   []any{map[string]any{"handler": downModule, "log_only": true}},
			"terminal": true,
		})
		server["errors"] = map[string]any{"routes": downRoutes}
	}
	servers := map[string]any{"nearhost": listenerWrappers(server)}
	if len(all) == 0 {
		// For a server with no host at all, automatic HTTPS adds a redirect
		// of every host to the http port, ahead of our routes when another
		// server (the web port's) has a certificate subject, and that would
		// answer any client asking for any name. With no host there is
		// nothing to redirect.
		server["automatic_https"] = map[string]any{"disable_redirects": true}
	}
	if len(patterns) > 0 {
		// Automatic HTTPS would otherwise try to manage the patterns as
		// subjects. Its redirects stay on, but on the http port our own
		// routes answer first (see httpServer).
		server["automatic_https"] = map[string]any{"skip_certificates": patterns}
	}
	servers["nearhost_http"] = listenerWrappers(httpServer(hosts, union, httpPort, httpsPort))
	if web {
		servers["nearhost_web"] = listenerWrappers(webServer(opts))
	}

	cfg := map[string]any{
		// persist=false stops Caddy autosaving this config over the user's own
		// Caddy install.
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"logging": map[string]any{"logs": logs},
		"storage": map[string]any{"module": storageModule, "root": opts.StorageRoot},
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
						"name":                  CAName,
						"install_trust":         false,
						"intermediate_lifetime": caddyDuration(intermediateLifetime),
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
