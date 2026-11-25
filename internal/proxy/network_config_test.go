package proxy

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/netbind"
)

const netDomain = "lab.example.dev"

// netSites are a plain site, a site with extra clients (Docker), and a
// site on the network domain with an exact host and a pattern.
var netSites = []Site{
	{Alias: "a.test", Hosts: []string{"a.test", "*.local.x.io"}, Upstream: "127.0.0.1:1"},
	{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2", Clients: []string{"172.17.0.0/16"}},
	{Alias: "app." + netDomain, Hosts: []string{"app." + netDomain, "*.pr." + netDomain}, Upstream: "127.0.0.1:3"},
}

// netOptions are the options of a networked core on the network domain
// with the default allow list, listening, with certificates from the CA.
func netOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		StorageRoot: t.TempDir(),
		HTTPSPort:   8443,
		HTTPPort:    8080,
		WebPort:     7443,
		Version:     "1.2.3",
		Network: &NetworkOptions{
			Domain:       netDomain,
			Certificates: "ca",
			Allow:        config.DefaultNetworkAllow,
			Listen:       true,
		},
	}
}

func netJSON(t *testing.T, sites []Site, opts Options) map[string]any {
	t.Helper()
	raw, err := BuildConfig(sites, opts)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := decodeConfig(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func allowRanges(t *testing.T) []string {
	t.Helper()
	r, err := clientRanges(config.DefaultNetworkAllow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func joinRanges(t *testing.T, clients []string, withAllow bool) []string {
	t.Helper()
	out := slices.Clone(loopbackRanges)
	if withAllow {
		out = append(out, allowRanges(t)...)
	}
	extra, err := clientRanges(clients)
	if err != nil {
		t.Fatal(err)
	}
	return append(out, extra...)
}

// routeFor is the route of server whose match names host.
func routeFor(t *testing.T, routes any, host string) map[string]any {
	t.Helper()
	for _, r := range routes.([]any) {
		m := r.(map[string]any)
		match, ok := m["match"].([]any)
		if !ok || len(match) == 0 {
			continue
		}
		hosts, ok := match[0].(map[string]any)["host"].([]any)
		if !ok || len(hosts) != 1 || hosts[0] != host {
			continue
		}
		if _, ok := match[0].(map[string]any)["remote_ip"]; ok {
			return m
		}
	}
	t.Fatalf("no route for %s", host)
	return nil
}

func handlerNames(t *testing.T, route map[string]any) []string {
	t.Helper()
	var out []string
	for _, h := range route["handle"].([]any) {
		out = append(out, h.(map[string]any)["handler"].(string))
	}
	return out
}

// underNet reports whether h is the network domain or one label beneath
// it: a name the ACME wildcard covers.
func underNet(h string) bool {
	label, ok := strings.CutSuffix(h, "."+netDomain)
	return h == netDomain || ok && !strings.Contains(label, ".")
}

// With networked mode off, or with no domain, the config is byte for byte
// the one without the option.
func TestNetworkOffIsUnchanged(t *testing.T) {
	sites := []Site{
		{Alias: "a.test", Hosts: []string{"a.test", "*.local.x.io", "internal.*.local.x.io"}, Upstream: "127.0.0.1:1"},
		{Alias: "b.test", Hosts: []string{"b.test", "api.local.x.io"}, Upstream: "127.0.0.1:2"},
	}
	for _, web := range []int{0, 7443} {
		base := Options{StorageRoot: "/tmp/store", HTTPSPort: 8443, HTTPPort: 8080, Capture: true, Version: "1.2.3", WebPort: web}
		want, err := BuildConfig(sites, base)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []*NetworkOptions{nil, {Listen: false}, {Listen: true, Allow: config.DefaultNetworkAllow, Certificates: "acme", DNSToken: "tok"}} {
			opts := base
			opts.Network = n
			got, err := BuildConfig(sites, opts)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("web port %d, network %+v changed the config:\n got %s\nwant %s", web, n, got, want)
			}
		}
	}
}

// The page listens on every address, answers at the network's page host
// too, and admits allowed clients both at the handshake and in its route.
func TestNetworkPageServesNetworkHostToAllowedClients(t *testing.T) {
	cfg := netJSON(t, netSites, netOptions(t))
	web := dig(t, cfg, "apps", "http", "servers", "nearhost_web")
	if got, want := strs(dig(t, web, "listen")), listenAddrs(netbind.NetworkAddrs(7443)); !reflect.DeepEqual(got, want) {
		t.Errorf("page listen = %v, want %v", got, want)
	}
	ranges := joinRanges(t, nil, true)
	if got, want := strs(dig(t, web, "routes", 0, "match", 0, "host")), append(pageHosts(), "nearhost."+netDomain); !reflect.DeepEqual(got, want) {
		t.Errorf("page hosts = %v, want %v", got, want)
	}
	if got := strs(dig(t, web, "routes", 0, "match", 0, "remote_ip", "ranges")); !reflect.DeepEqual(got, ranges) {
		t.Errorf("page ranges = %v, want %v", got, ranges)
	}
	if got := strs(dig(t, web, "tls_connection_policies", 0, "match", "remote_ip", "ranges")); !reflect.DeepEqual(got, ranges) {
		t.Errorf("page TLS ranges = %v, want %v", got, ranges)
	}
	if got := dig(t, web, "routes", 0, "handle", 0, "network_host"); got != "nearhost."+netDomain {
		t.Errorf("home network_host = %v", got)
	}
	if got := networkPageHost(netOptions(t).Network); got != "nearhost."+netDomain {
		t.Errorf("networkPageHost = %q", got)
	}
}

// Each site admits the allowed clients besides its own, and runs the
// device guard right after the tag, with loopback and its own clients as
// local; its error route runs the guard too.
func TestNetworkSitesAdmitAllowAndRunDeviceGuard(t *testing.T) {
	cfg := netJSON(t, netSites, netOptions(t))
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost")
	page := "https://nearhost." + netDomain + ":7443"
	for _, s := range netSites {
		for _, h := range s.Hosts {
			r := routeFor(t, dig(t, srv, "routes"), h)
			if got, want := strs(dig(t, r, "match", 0, "remote_ip", "ranges")), joinRanges(t, s.Clients, true); !reflect.DeepEqual(got, want) {
				t.Errorf("%s ranges = %v, want %v", h, got, want)
			}
			if got, want := handlerNames(t, r), []string{tagModule, deviceModule, "headers", "reverse_proxy"}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s handlers = %v, want %v", h, got, want)
			}
			if got := dig(t, r, "handle", 1, "page"); got != page {
				t.Errorf("%s device page = %v, want %s", h, got, page)
			}
			if got, want := strs(dig(t, r, "handle", 1, "local")), joinRanges(t, s.Clients, false); !reflect.DeepEqual(got, want) {
				t.Errorf("%s device local = %v, want %v", h, got, want)
			}
			down := routeFor(t, dig(t, srv, "errors", "routes"), h)
			if got, want := handlerNames(t, down), []string{deviceModule, downModule}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s error handlers = %v, want %v", h, got, want)
			}
			if got, want := strs(dig(t, down, "match", 0, "remote_ip", "ranges")), joinRanges(t, s.Clients, true); !reflect.DeepEqual(got, want) {
				t.Errorf("%s error ranges = %v, want %v", h, got, want)
			}
		}
	}
	if got, want := strs(dig(t, srv, "listen")), listenAddrs(netbind.NetworkAddrs(8443)); !reflect.DeepEqual(got, want) {
		t.Errorf("https listen = %v, want %v", got, want)
	}
	if got, want := strs(dig(t, srv, "tls_connection_policies", 0, "match", "remote_ip", "ranges")), joinRanges(t, nil, true); !reflect.DeepEqual(got, want) {
		t.Errorf("https first TLS ranges = %v, want %v", got, want)
	}
}

// The http server redirects allowed clients rather than dropping them.
func TestNetworkHTTPRedirectsAllowedClients(t *testing.T) {
	cfg := netJSON(t, netSites, netOptions(t))
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost_http")
	union := strs(dig(t, srv, "routes", 0, "match", 0, "not", 0, "remote_ip", "ranges"))
	for _, r := range allowRanges(t) {
		if !slices.Contains(union, r) {
			t.Errorf("http union %v lacks %s", union, r)
		}
	}
	if got, want := strs(dig(t, srv, "listen")), listenAddrs(netbind.NetworkAddrs(8080)); !reflect.DeepEqual(got, want) {
		t.Errorf("http listen = %v, want %v", got, want)
	}
	r := routeFor(t, dig(t, srv, "routes"), "a.test")
	if got, want := strs(dig(t, r, "match", 0, "remote_ip", "ranges")), joinRanges(t, nil, true); !reflect.DeepEqual(got, want) {
		t.Errorf("a.test redirect ranges = %v, want %v", got, want)
	}
}

// With ACME, the domain and its wildcard come from Let's Encrypt over the
// Cloudflare DNS challenge, and the local CA issues for no name the
// wildcard covers.
func TestNetworkACMEPolicy(t *testing.T) {
	opts := netOptions(t)
	opts.Network.Certificates = "acme"
	opts.Network.DNSToken = "tok"
	cfg := netJSON(t, netSites, opts)
	auto := dig(t, cfg, "apps", "tls", "automation")
	first := dig(t, auto, "policies", 0)
	if got, want := strs(dig(t, first, "subjects")), []string{"*." + netDomain, netDomain}; !reflect.DeepEqual(got, want) {
		t.Errorf("ACME subjects = %v, want %v", got, want)
	}
	wantIssuer := map[string]any{"module": "acme", "challenges": map[string]any{"dns": map[string]any{
		"provider": map[string]any{"name": "cloudflare", "api_token": "tok"},
	}}}
	if got := dig(t, first, "issuers"); !reflect.DeepEqual(got, []any{wantIssuer}) {
		t.Errorf("ACME issuers = %v, want [%v]", got, wantIssuer)
	}
	for i, p := range dig(t, auto, "policies").([]any)[1:] {
		if dig(t, p, "issuers", 0, "module") != "internal" {
			t.Errorf("policy %d is not the local CA's: %v", i+1, p)
		}
		if subj, ok := p.(map[string]any)["subjects"]; ok {
			for _, s := range strs(subj) {
				if underNet(s) {
					t.Errorf("internal policy has subject %s, which the wildcard covers", s)
				}
			}
		}
	}
	for _, p := range strs(dig(t, auto, "on_demand", "permission", "patterns")) {
		if underNet(p) {
			t.Errorf("on-demand pattern %s is one the wildcard covers", p)
		}
	}
	if got, want := strs(dig(t, cfg, "apps", "tls", "certificates", "automate")), []string{"*." + netDomain}; !reflect.DeepEqual(got, want) {
		t.Errorf("automated certificates = %v, want %v", got, want)
	}
}

// With the local CA, any name under the domain gets a certificate on
// demand, and the page's network host is a subject.
func TestNetworkCAPolicyCoversDomainOnDemand(t *testing.T) {
	cfg := netJSON(t, netSites, netOptions(t))
	auto := dig(t, cfg, "apps", "tls", "automation")
	if got := strs(dig(t, auto, "on_demand", "permission", "patterns")); !slices.Contains(got, "*."+netDomain) {
		t.Errorf("on-demand patterns %v lack *.%s", got, netDomain)
	}
	if got := strs(dig(t, auto, "policies", 0, "subjects")); !slices.Contains(got, "nearhost."+netDomain) {
		t.Errorf("internal subjects %v lack nearhost.%s", got, netDomain)
	}
	for i, p := range dig(t, auto, "policies").([]any) {
		if dig(t, p, "issuers", 0, "module") != "internal" {
			t.Errorf("policy %d is not the local CA's: %v", i, p)
		}
	}
	if _, ok := dig(t, cfg, "apps", "tls").(map[string]any)["certificates"]; ok {
		t.Error("the local CA automates certificates of its own")
	}
}

// Without Listen, the listeners and client ranges are those of a core that
// is not networked: only the page host and the certificates change, so
// Listen alone later widens them.
func TestNetworkListenFalseKeepsLoopback(t *testing.T) {
	on := netOptions(t)
	on.Network.Listen = false
	off := on
	off.Network = nil
	got := netJSON(t, netSites, on)
	want := netJSON(t, netSites, off)
	for _, name := range []string{"nearhost", "nearhost_http", "nearhost_web"} {
		g := dig(t, got, "apps", "http", "servers", name)
		w := dig(t, want, "apps", "http", "servers", name)
		if !reflect.DeepEqual(dig(t, g, "listen"), dig(t, w, "listen")) {
			t.Errorf("%s listen = %v, want %v", name, dig(t, g, "listen"), dig(t, w, "listen"))
		}
		if !reflect.DeepEqual(dig(t, g, "tls_connection_policies"), dig(t, w, "tls_connection_policies")) {
			t.Errorf("%s TLS policies changed", name)
		}
	}
	for _, name := range []string{"nearhost", "nearhost_http"} {
		if g, w := dig(t, got, "apps", "http", "servers", name), dig(t, want, "apps", "http", "servers", name); !reflect.DeepEqual(g, w) {
			t.Errorf("%s changed without Listen:\n got %v\nwant %v", name, g, w)
		}
	}
	web := dig(t, got, "apps", "http", "servers", "nearhost_web")
	if got, want := strs(dig(t, web, "routes", 0, "match", 0, "host")), append(pageHosts(), "nearhost."+netDomain); !reflect.DeepEqual(got, want) {
		t.Errorf("page hosts = %v, want %v", got, want)
	}
	if got := strs(dig(t, web, "routes", 0, "match", 0, "remote_ip", "ranges")); !reflect.DeepEqual(got, loopbackRanges) {
		t.Errorf("page ranges = %v, want loopback", got)
	}
	listening := netJSON(t, netSites, netOptions(t))
	if !reflect.DeepEqual(dig(t, got, "apps", "tls"), dig(t, listening, "apps", "tls")) {
		t.Error("Listen changed the certificates")
	}
}

// An allow entry that is not a CIDR fails the build.
func TestNetworkRejectsBadAllow(t *testing.T) {
	opts := netOptions(t)
	opts.Network.Allow = []string{"192.168.1.1"}
	if _, err := BuildConfig(netSites, opts); err == nil || !strings.Contains(err.Error(), "192.168.1.1") {
		t.Errorf("BuildConfig with a bad allow = %v", err)
	}
}

// A networked proxy waits for its ports on every address it listens on.
func TestPortsIncludeNetworkAddrs(t *testing.T) {
	p := New(Options{StorageRoot: t.TempDir(), HTTPSPort: 8443, HTTPPort: 8080, WebPort: 6327})
	p.SetNetwork(&NetworkOptions{Domain: netDomain, Listen: true, Allow: config.DefaultNetworkAllow})
	want := []listenPort{
		{8443, netbind.NetworkAddrs(8443)},
		{8080, netbind.NetworkAddrs(8080)},
		{6327, netbind.NetworkAddrs(6327)},
	}
	if got := p.ports(); !reflect.DeepEqual(got, want) {
		t.Errorf("ports = %v, want %v", got, want)
	}
	p.SetNetwork(nil)
	if got := p.ports(); !reflect.DeepEqual(got[2], listenPort{6327, pageAddrs(6327)}) {
		t.Errorf("page port after SetNetwork(nil) = %v", got[2])
	}
}

// Caddy takes the networked config with either source of certificates:
// the device guard and the Cloudflare DNS provider are compiled in.
func TestNetworkConfigValidates(t *testing.T) {
	for _, certs := range []string{"ca", "acme"} {
		opts := netOptions(t)
		opts.Network.Certificates = certs
		// A token of the shape Cloudflare's provider checks for.
		opts.Network.DNSToken = strings.Repeat("x", 40)
		raw, err := BuildConfig(netSites, opts)
		if err != nil {
			t.Fatal(err)
		}
		var cfg caddy.Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if err := caddy.Validate(&cfg); err != nil {
			t.Errorf("%s: Caddy refuses the networked config: %v", certs, err)
		}
	}
}

// With ACME, a name two or more labels beneath the domain, which the
// wildcard does not cover, stays on the local CA: as a subject, or on
// demand for a pattern.
func TestNetworkACMEKeepsDeepNamesOnLocalCA(t *testing.T) {
	opts := netOptions(t)
	opts.Network.Certificates = "acme"
	opts.Network.DNSToken = "tok"
	sites := []Site{{Alias: "app." + netDomain, Hosts: []string{"app." + netDomain, "x.app." + netDomain, "*.pr." + netDomain, "*." + netDomain}, Upstream: "127.0.0.1:3"}}
	cfg := netJSON(t, sites, opts)
	auto := dig(t, cfg, "apps", "tls", "automation")
	subjects := strs(dig(t, auto, "policies", 1, "subjects"))
	if dig(t, auto, "policies", 1, "issuers", 0, "module") != "internal" {
		t.Fatalf("second policy is not the local CA's")
	}
	if slices.Contains(subjects, "app."+netDomain) {
		t.Errorf("internal subjects %v have app.%s, which the wildcard covers", subjects, netDomain)
	}
	if !slices.Contains(subjects, "x.app."+netDomain) {
		t.Errorf("internal subjects %v lack x.app.%s", subjects, netDomain)
	}
	if got, want := strs(dig(t, auto, "on_demand", "permission", "patterns")), []string{"*.pr." + netDomain}; !reflect.DeepEqual(got, want) {
		t.Errorf("on-demand patterns = %v, want %v", got, want)
	}
}
