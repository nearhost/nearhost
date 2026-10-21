package proxy

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("expected object at %v, got %T", p, v)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("expected array index %d, got %T", k, v)
			}
			v = a[k]
		}
	}
	return v
}

func TestBuildConfigProducesRoutesTLSAndLogs(t *testing.T) {
	raw, err := BuildConfig([]Site{
		{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:8080"},
		{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:5000"},
	}, Options{StorageRoot: "/tmp/store", HTTPSPort: 8443, HTTPPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, raw)
	}
	if dig(t, cfg, "admin", "disabled") != true {
		t.Error("admin should be disabled")
	}
	if dig(t, cfg, "storage", "root") != "/tmp/store" || dig(t, cfg, "storage", "module") != "file_system" {
		t.Error("storage not set")
	}
	if dig(t, cfg, "apps", "http", "https_port") != float64(8443) || dig(t, cfg, "apps", "http", "http_port") != float64(8080) {
		t.Error("ports not set")
	}
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost")
	if dig(t, srv, "listen", 0) != ":8443" {
		t.Error("listen not set")
	}
	if dig(t, srv, "routes", 0, "match", 0, "host", 0) != "a.test" {
		t.Error("route host mismatch")
	}
	if tag := dig(t, srv, "routes", 0, "handle", 0); dig(t, tag, "handler") != tagModule || dig(t, tag, "alias") != "a.test" {
		t.Errorf("first handler should tag the route with its alias: %v", tag)
	}
	handler := proxyHandler(t, dig(t, srv, "routes", 0))
	if dig(t, handler, "handler") != "reverse_proxy" || dig(t, handler, "upstreams", 0, "dial") != "127.0.0.1:8080" {
		t.Errorf("reverse proxy wrong: %v", handler)
	}
	if dig(t, handler, "flush_interval") != float64(-1) {
		t.Error("flush_interval should be -1 for streaming")
	}
	if dig(t, srv, "routes", 1, "match", 0, "host", 0) != "b.test" {
		t.Error("second route missing")
	}
	if logs := dig(t, srv, "logs").(map[string]any); len(logs) != 1 || logs["default_logger_name"] != "nearhost" {
		t.Errorf("server logs = %v, want only the default logger name", logs)
	}
	subjects := dig(t, cfg, "apps", "tls", "automation", "policies", 0, "subjects").([]any)
	if !slices.Equal(strs(subjects), []string{"a.test", "b.test", "localhost"}) {
		t.Errorf("subjects = %v", subjects)
	}
	if dig(t, cfg, "apps", "tls", "automation", "policies", 0, "issuers", 0, "module") != "internal" {
		t.Error("internal issuer missing")
	}
	ca := dig(t, cfg, "apps", "pki", "certificate_authorities", "local")
	if dig(t, ca, "name") != CAName || dig(t, ca, "install_trust") != false {
		t.Errorf("pki wrong: %v", ca)
	}
	if logs := dig(t, cfg, "logging", "logs").(map[string]any); len(logs) != 2 {
		t.Errorf("logs = %v, want default and access only", logs)
	}
	access := dig(t, cfg, "logging", "logs", "access")
	if dig(t, access, "writer", "output") != "nearhost" || dig(t, access, "writer", "access") != true ||
		dig(t, access, "include", 0) != "http.log.access.nearhost" || len(dig(t, access, "include").([]any)) != 1 {
		t.Errorf("access log wrong: %v", access)
	}
	if _, ok := dig(t, access, "writer").(map[string]any)["alias"]; ok {
		t.Errorf("the access writer takes the alias from each line, not its config: %v", access)
	}
	def := dig(t, cfg, "logging", "logs", "default")
	if dig(t, def, "writer", "output") != "nearhost" || dig(t, def, "exclude", 0) != "http.log.access" {
		t.Errorf("default log wrong: %v", def)
	}
}

func TestBuildConfigDefaultsPorts(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	if dig(t, cfg, "apps", "http", "https_port") != float64(443) || dig(t, cfg, "apps", "http", "http_port") != float64(80) {
		t.Error("default ports wrong")
	}
}

func TestBuildConfigNoSitesHasNoListenersButKeepsCA(t *testing.T) {
	raw, err := BuildConfig(nil, Options{StorageRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	apps := dig(t, cfg, "apps").(map[string]any)
	if _, ok := apps["http"]; ok {
		t.Error("zero-site config must not include the http app")
	}
	if dig(t, cfg, "apps", "pki", "certificate_authorities", "local", "name") != CAName {
		t.Error("zero-site config must still configure the local CA")
	}
	if dig(t, cfg, "admin", "disabled") != true || dig(t, cfg, "apps", "tls", "disable_storage_clean") != true {
		t.Error("zero-site config must keep admin disabled and storage clean off")
	}
}

// With HomeWithNoSites, a config with no sites still listens, to serve the
// home page at localhost alone.
func TestBuildConfigHomeWithNoSites(t *testing.T) {
	raw, err := BuildConfig(nil, Options{StorageRoot: t.TempDir(), HomeWithNoSites: true})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := routeHosts(cfg); !slices.Equal(got, []string{"localhost"}) {
		t.Fatalf("route hosts = %v, want localhost alone", got)
	}
	if len(homeRoutes(t, cfg)) != 1 {
		t.Error("no home page route")
	}
	if got := tlsSubjects(cfg); !slices.Equal(got, []string{"localhost"}) {
		t.Errorf("subjects = %v, want localhost", got)
	}
}

func TestBuildConfigRejectsBadInput(t *testing.T) {
	if _, err := BuildConfig([]Site{{Alias: "", Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/x"}); err == nil {
		t.Error("empty alias should fail")
	}
	if _, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: ""}}, Options{StorageRoot: "/x"}); err == nil {
		t.Error("empty upstream should fail")
	}
	if _, err := BuildConfig([]Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/x"}); err == nil {
		t.Error("a site with no hosts should fail")
	}
	if _, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test", "*.com"}, Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/x"}); err == nil || !strings.Contains(err.Error(), `"*.com"`) {
		t.Errorf("an invalid host should fail naming it, got %v", err)
	}
	if _, err := BuildConfig(nil, Options{}); err == nil {
		t.Error("empty storage root should fail")
	}
}

func build(t *testing.T, sites []Site) map[string]any {
	t.Helper()
	raw, err := BuildConfig(sites, Options{StorageRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBuildConfigDoesNotPersistAutosave(t *testing.T) {
	cfg := build(t, nil)
	if dig(t, cfg, "admin", "config", "persist") != false {
		t.Error("admin.config.persist must be false so the user's Caddy autosave is untouched")
	}
	if dig(t, cfg, "admin", "disabled") != true {
		t.Error("admin should be disabled")
	}
}

func TestBuildConfigSetsForwardingHeaders(t *testing.T) {
	cfg := build(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:8080"}})
	h := proxyHandler(t, dig(t, cfg, "apps", "http", "servers", "nearhost", "routes", 0))
	set := dig(t, h, "headers", "request", "set").(map[string]any)
	if dig(t, set, "X-Forwarded-Proto", 0) != "https" {
		t.Errorf("X-Forwarded-Proto not https: %v", set)
	}
	if dig(t, set, "X-Forwarded-For", 0) != "{http.request.remote.host}" {
		t.Errorf("X-Forwarded-For not set: %v", set)
	}
	if _, ok := set["Host"]; ok {
		t.Error("Host header must pass through unchanged")
	}
}

func TestBuildConfigKeepsAutomaticHTTPSRedirect(t *testing.T) {
	cfg := build(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost").(map[string]any)
	if a, ok := srv["automatic_https"]; ok {
		t.Errorf("automatic_https must stay default so port 80 redirects: %v", a)
	}
	if l := srv["listen"].([]any); len(l) != 1 || l[0] != ":443" {
		t.Errorf("listen = %v, want only :443", l)
	}
}

func TestBuildConfigIsDeterministicAndSorted(t *testing.T) {
	a := []Site{{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2"}, {Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}
	b := []Site{a[1], a[0]}
	ra, _ := BuildConfig(a, Options{StorageRoot: "/s"})
	rb, _ := BuildConfig(b, Options{StorageRoot: "/s"})
	if string(ra) != string(rb) {
		t.Error("output depends on input order")
	}
	cfg := build(t, a)
	if dig(t, cfg, "apps", "http", "servers", "nearhost", "routes", 0, "match", 0, "host", 0) != "a.test" {
		t.Error("routes not sorted by alias")
	}
	if a[0].Alias != "b.test" {
		t.Error("input slice was mutated")
	}
}

// One project with several web routes is several sites under one alias,
// each serving its own hosts.
func TestBuildConfigAllowsSitesSharingAnAlias(t *testing.T) {
	cfg := buildJSON(t, []Site{
		{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"},
		{Alias: "a.test", Hosts: []string{"api.example.io"}, Upstream: "127.0.0.1:2"},
	})
	if got := routeHosts(cfg); !slices.Equal(got, []string{"a.test", "api.example.io", "localhost"}) {
		t.Fatalf("route hosts = %v", got)
	}
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes")
	for i, dial := range []string{"127.0.0.1:1", "127.0.0.1:2"} {
		if got := dig(t, proxyHandler(t, dig(t, routes, i)), "upstreams", 0, "dial"); got != dial {
			t.Errorf("route %d dials %v, want %s", i, got, dial)
		}
		if got := dig(t, routes, i, "handle", 0, "alias"); got != "a.test" {
			t.Errorf("route %d tagged %v, want a.test", i, got)
		}
	}
}

func TestBuildConfigRejectsDuplicateHostOnOneSite(t *testing.T) {
	_, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test", "a.test"}, Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/s"})
	if err == nil || !strings.Contains(err.Error(), `"a.test"`) {
		t.Errorf("err = %v, want one naming the host", err)
	}
}

func TestBuildConfigNoSitesHasNoEmptyTLSPolicy(t *testing.T) {
	cfg := build(t, nil)
	if p := dig(t, cfg, "apps", "tls", "automation", "policies").([]any); len(p) != 0 {
		t.Errorf("expected no policies with zero sites, got %v", p)
	}
	if dig(t, cfg, "apps", "pki", "certificate_authorities", "local", "name") != CAName {
		t.Error("CA must be configured even with zero sites")
	}
}

func TestBuildConfigRejectsAliasesDifferingOnlyByCase(t *testing.T) {
	// "B.test" sorts between the two spellings, so they are not adjacent.
	sites := []Site{
		{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"},
		{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2"},
		{Alias: "c.test", Hosts: []string{"c.test", "A.TEST"}, Upstream: "127.0.0.1:3"},
	}
	if _, err := BuildConfig(sites, Options{StorageRoot: "/x"}); err == nil {
		t.Error("aliases naming the same host should fail")
	}
}

func TestBuildConfigKeepsUpgradedStreamsAcrossReloads(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	handler := proxyHandler(t, dig(t, cfg, "apps", "http", "servers", "nearhost", "routes", 0))
	if dig(t, handler, "stream_close_delay") != "24h" {
		t.Errorf("stream_close_delay = %v, want 24h so WebSockets survive a reload", dig(t, handler, "stream_close_delay"))
	}
}

func TestBuildConfigDisablesStorageClean(t *testing.T) {
	for _, sites := range [][]Site{nil, {{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}} {
		raw, err := BuildConfig(sites, Options{StorageRoot: "/tmp/store"})
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if dig(t, cfg, "apps", "tls", "disable_storage_clean") != true {
			t.Errorf("disable_storage_clean not true with %d sites", len(sites))
		}
	}
}

func TestBuildConfigServesSitesToLoopbackClientsOnly(t *testing.T) {
	cfg := build(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}, {Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2"}})
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes").([]any)
	if len(routes) != 4 {
		t.Fatalf("got %d routes, want one per site, the home page and a final catch-all", len(routes))
	}
	for i, alias := range []string{"a.test", "b.test"} {
		sets := dig(t, routes[i], "match").([]any)
		// One set: its matchers must all hold. Two sets would be alternatives.
		if len(sets) != 1 {
			t.Fatalf("route %d has %d match sets, want one holding both host and remote_ip", i, len(sets))
		}
		if dig(t, sets[0], "host", 0) != alias {
			t.Errorf("route %d host = %v, want %s", i, dig(t, sets[0], "host"), alias)
		}
		ranges, _ := dig(t, sets[0], "remote_ip", "ranges").([]any)
		if len(ranges) != len(loopbackRanges) {
			t.Fatalf("route %d remote_ip ranges = %v, want %v", i, ranges, loopbackRanges)
		}
		for j, r := range ranges {
			if r != loopbackRanges[j] {
				t.Errorf("route %d range %d = %v, want %s", i, j, r, loopbackRanges[j])
			}
		}
	}
	// The handshake is refused first: the only TLS connection policy is for
	// loopback clients, so anyone else is shown no certificate.
	policies, _ := dig(t, cfg, "apps", "http", "servers", "nearhost", "tls_connection_policies").([]any)
	if len(policies) != 1 {
		t.Fatalf("got %d TLS connection policies, want one", len(policies))
	}
	policy, _ := policies[0].(map[string]any)
	if match, _ := policy["match"].(map[string]any); len(policy) != 1 || len(match) != 1 {
		t.Errorf("the TLS connection policy must hold the client address matcher and nothing else, got %v", policy)
	}
	tlsRanges, _ := dig(t, policy, "match", "remote_ip", "ranges").([]any)
	if len(tlsRanges) != len(loopbackRanges) {
		t.Fatalf("TLS connection policy ranges = %v, want %v", tlsRanges, loopbackRanges)
	}
	for j, r := range tlsRanges {
		if r != loopbackRanges[j] {
			t.Errorf("TLS connection policy range %d = %v, want %s", j, r, loopbackRanges[j])
		}
	}
	last := routes[3].(map[string]any)
	if m, ok := last["match"]; ok {
		t.Errorf("the final route must match every request, got match %v", m)
	}
	if dig(t, last, "handle", 0, "handler") != "static_response" || dig(t, last, "handle", 0, "abort") != true {
		t.Errorf("the final route must abort the connection, got %v", last["handle"])
	}
	if h := last["handle"].([]any); len(h) != 1 {
		t.Errorf("the final route has %d handlers, want one", len(h))
	}
}

func TestLoopbackRangesAreExactlyLoopback(t *testing.T) {
	want := []string{"127.0.0.0/8", "::1", "::ffff:127.0.0.0/104"}
	if len(loopbackRanges) != len(want) {
		t.Fatalf("loopbackRanges = %v, want %v", loopbackRanges, want)
	}
	for i := range want {
		if loopbackRanges[i] != want[i] {
			t.Fatalf("loopbackRanges = %v, want %v", loopbackRanges, want)
		}
	}
}

// buildJSON is BuildConfig for sites, decoded.
func buildJSON(t *testing.T, sites []Site) map[string]any {
	t.Helper()
	raw, err := BuildConfig(sites, Options{StorageRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// routeHosts is the first host each route matches, in order, leaving out
// the final route that drops everything else.
func routeHosts(cfg map[string]any) []string {
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["nearhost"].(map[string]any)
	var hosts []string
	for _, r := range srv["routes"].([]any) {
		m, ok := r.(map[string]any)["match"].([]any)
		if !ok {
			continue
		}
		hosts = append(hosts, m[0].(map[string]any)["host"].([]any)[0].(string))
	}
	return hosts
}

// tlsSubjects are the names of the first TLS automation policy.
func tlsSubjects(cfg map[string]any) []string {
	policies := cfg["apps"].(map[string]any)["tls"].(map[string]any)["automation"].(map[string]any)["policies"].([]any)
	var subjects []string
	for _, s := range policies[0].(map[string]any)["subjects"].([]any) {
		subjects = append(subjects, s.(string))
	}
	return subjects
}

// proxyHandler is the reverse_proxy handler of route.
func proxyHandler(t *testing.T, route any) any {
	t.Helper()
	for _, h := range dig(t, route, "handle").([]any) {
		if dig(t, h, "handler") == "reverse_proxy" {
			return h
		}
	}
	t.Fatalf("no reverse_proxy handler in %v", route)
	return nil
}

func TestBuildConfigOrdersHostsAcrossSites(t *testing.T) {
	cfg := buildJSON(t, []Site{
		{Alias: "pro.test", Hosts: []string{"pro.test", "*.local.redactr.pro", "internal.*.local.redactr.pro"}, Upstream: "127.0.0.1:8211"},
		{Alias: "platform.test", Hosts: []string{"platform.test", "auth.local.redactr.pro"}, Upstream: "127.0.0.1:8210"},
	})
	// One Caddy route per host, and the home page at localhost, then the
	// final abort route.
	want := []string{"auth.local.redactr.pro", "localhost", "platform.test", "pro.test", "internal.*.local.redactr.pro", "*.local.redactr.pro"}
	if got := routeHosts(cfg); !slices.Equal(got, want) {
		t.Fatalf("route hosts = %v, want %v", got, want)
	}
}

func TestBuildConfigRejectsHostOnTwoSites(t *testing.T) {
	_, err := BuildConfig([]Site{
		{Alias: "a.test", Hosts: []string{"a.test", "x.example.io"}, Upstream: "127.0.0.1:1"},
		{Alias: "b.test", Hosts: []string{"b.test", "X.example.io"}, Upstream: "127.0.0.1:2"},
	}, Options{StorageRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), `"x.example.io"`) {
		t.Fatalf("err = %v, want one naming the host", err)
	}
}

func TestBuildConfigLegacySiteServesAliasOnly(t *testing.T) {
	cfg := buildJSON(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
	if got := routeHosts(cfg); !slices.Equal(got, []string{"a.test", "localhost"}) {
		t.Fatalf("route hosts = %v", got)
	}
	if got := tlsSubjects(cfg); !slices.Equal(got, []string{"a.test", "localhost"}) {
		t.Fatalf("subjects = %v", got)
	}
}

// The tag comes first, so the alias is on the access log line of every
// request, including those the capture handler or the upstream fails.
func TestBuildConfigTagsBeforeCapture(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test", "app.example.io"}, Upstream: "127.0.0.1:1"}}, Options{StorageRoot: t.TempDir(), Capture: true})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes").([]any)
	for i := range 2 {
		handle := dig(t, routes[i], "handle").([]any)
		if len(handle) != 4 || dig(t, handle[0], "handler") != tagModule || dig(t, handle[0], "alias") != "a.test" ||
			dig(t, handle[1], "handler") != captureModule || dig(t, handle[2], "handler") != "headers" || dig(t, handle[3], "handler") != "reverse_proxy" {
			t.Errorf("route %d handlers = %v, want tag, capture, headers, reverse_proxy", i, handle)
		}
	}
}

// Patterns cannot be certificate subjects, so their certificates come on
// demand from a second policy, which only names the permission module allows.
func TestBuildConfigIssuesPatternsOnDemand(t *testing.T) {
	cfg := buildJSON(t, []Site{
		{Alias: "pro.test", Hosts: []string{"pro.test", "*.local.example.pro", "internal.*.local.example.pro"}, Upstream: "127.0.0.1:1"},
	})
	if got := tlsSubjects(cfg); !slices.Equal(got, []string{"localhost", "pro.test"}) {
		t.Errorf("first policy subjects = %v, want only the exact hosts", got)
	}
	policies := dig(t, cfg, "apps", "tls", "automation", "policies").([]any)
	if len(policies) != 2 {
		t.Fatalf("policies = %v, want two", policies)
	}
	second := policies[1].(map[string]any)
	if second["on_demand"] != true || second["subjects"] != nil || dig(t, second, "issuers", 0, "module") != "internal" {
		t.Errorf("second policy = %v, want on_demand with the internal issuer and no subjects", second)
	}
	perm := dig(t, cfg, "apps", "tls", "automation", "on_demand", "permission").(map[string]any)
	if perm["module"] != "nearhost" || !slices.Equal(strs(perm["patterns"]), []string{"internal.*.local.example.pro", "*.local.example.pro"}) {
		t.Errorf("on_demand permission = %v", perm)
	}
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost").(map[string]any)
	if got := strs(dig(t, srv, "automatic_https", "skip_certificates")); !slices.Equal(got, []string{"internal.*.local.example.pro", "*.local.example.pro"}) {
		t.Errorf("skip_certificates = %v", got)
	}
	if v, ok := srv["automatic_https"].(map[string]any)["disable_redirects"]; ok {
		t.Errorf("redirects must stay on, got disable_redirects = %v", v)
	}
}

// The http port drops clients that are not on loopback first, then
// redirects each served host, exact or pattern, for loopback clients, and
// drops whatever is left, so the routes Caddy adds after ours never answer.
func TestBuildConfigRedirectsEveryHostFromHTTP(t *testing.T) {
	raw, err := BuildConfig([]Site{
		{Alias: "pro.test", Hosts: []string{"pro.test", "*.local.example.pro"}, Upstream: "127.0.0.1:1"},
	}, Options{StorageRoot: t.TempDir(), HTTPPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost_http").(map[string]any)
	if got := strs(srv["listen"]); !slices.Equal(got, []string{":8080"}) {
		t.Errorf("listen = %v, want :8080", got)
	}
	routes := srv["routes"].([]any)
	if len(routes) != 5 {
		t.Fatalf("routes = %v, want five", routes)
	}
	aborts := func(r any) bool {
		h := dig(t, r, "handle", 0).(map[string]any)
		return h["handler"] == "static_response" && h["abort"] == true
	}
	if got := strs(dig(t, routes[0], "match", 0, "not", 0, "remote_ip", "ranges")); !slices.Equal(got, loopbackRanges) || !aborts(routes[0]) {
		t.Errorf("first route = %v, want an abort for clients not on loopback", routes[0])
	}
	for i, host := range []string{"localhost", "pro.test", "*.local.example.pro"} {
		r := routes[i+1]
		if got := strs(dig(t, r, "match", 0, "host")); !slices.Equal(got, []string{host}) {
			t.Errorf("route %d host = %v, want %s", i+1, got, host)
		}
		if got := strs(dig(t, r, "match", 0, "remote_ip", "ranges")); !slices.Equal(got, loopbackRanges) {
			t.Errorf("route %d remote_ip = %v, want loopback", i+1, got)
		}
		h := dig(t, r, "handle", 0).(map[string]any)
		if h["handler"] != "static_response" || h["status_code"] != float64(308) ||
			dig(t, h, "headers", "Location", 0) != "https://{http.request.host}{http.request.uri}" {
			t.Errorf("route %d handler = %v, want a 308 to https", i+1, h)
		}
	}
	if last := routes[4].(map[string]any); last["match"] != nil || !aborts(last) {
		t.Errorf("last route = %v, want an abort with no matchers", last)
	}
}

// The http port gets the same server without patterns, as Caddy's own
// redirect server would answer any client.
func TestBuildConfigHasHTTPServerWithoutPatterns(t *testing.T) {
	cfg := buildJSON(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost_http", "routes").([]any)
	if len(routes) != 4 || !slices.Equal(strs(dig(t, routes[1], "match", 0, "host")), []string{"a.test"}) ||
		!slices.Equal(strs(dig(t, routes[2], "match", 0, "host")), []string{"localhost"}) {
		t.Errorf("routes = %v, want the abort, a.test's and localhost's redirects and the abort", routes)
	}
}

func TestBuildConfigWithoutPatternsHasNoOnDemand(t *testing.T) {
	cfg := buildJSON(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
	if _, ok := dig(t, cfg, "apps", "tls", "automation").(map[string]any)["on_demand"]; ok {
		t.Error("on_demand present without patterns")
	}
	if n := len(dig(t, cfg, "apps", "tls", "automation", "policies").([]any)); n != 1 {
		t.Errorf("%d policies, want 1", n)
	}
}

func strs(v any) []string {
	var out []string
	a, _ := v.([]any)
	for _, s := range a {
		out = append(out, s.(string))
	}
	return out
}

// homeRoutes are the routes of the https server whose handler is the home
// page.
func homeRoutes(t *testing.T, cfg map[string]any) []any {
	t.Helper()
	var out []any
	for _, r := range dig(t, cfg, "apps", "http", "servers", "nearhost", "routes").([]any) {
		for _, h := range dig(t, r, "handle").([]any) {
			if dig(t, h, "handler") == homeModule {
				out = append(out, r)
			}
		}
	}
	return out
}

// While no project serves localhost, the proxy serves its own page there,
// to loopback clients, with a certificate from the internal CA and a
// redirect from http, like any exact host.
func TestBuildConfigServesHomePageAtLocalhost(t *testing.T) {
	raw, err := BuildConfig([]Site{
		{Alias: "pro.test", Hosts: []string{"pro.test", "*.local.example.pro"}, Upstream: "127.0.0.1:1"},
	}, Options{StorageRoot: t.TempDir(), Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := routeHosts(cfg); !slices.Equal(got, []string{"localhost", "pro.test", "*.local.example.pro"}) {
		t.Fatalf("route hosts = %v, want localhost among the exact hosts", got)
	}
	home := homeRoutes(t, cfg)
	if len(home) != 1 {
		t.Fatalf("%d home page routes, want one", len(home))
	}
	r := home[0].(map[string]any)
	if got := strs(dig(t, r, "match", 0, "host")); !slices.Equal(got, []string{"localhost"}) {
		t.Errorf("home route host = %v, want localhost", got)
	}
	if got := strs(dig(t, r, "match", 0, "remote_ip", "ranges")); !slices.Equal(got, loopbackRanges) {
		t.Errorf("home route remote_ip = %v, want loopback", got)
	}
	handle := dig(t, r, "handle").([]any)
	if len(handle) != 1 || dig(t, handle[0], "version") != "1.2.3" {
		t.Errorf("home route handlers = %v, want the home page alone, with the version", handle)
	}
	if r["terminal"] != true {
		t.Error("home route must be terminal")
	}
	if got := tlsSubjects(cfg); !slices.Equal(got, []string{"localhost", "pro.test"}) {
		t.Errorf("subjects = %v, want localhost and pro.test", got)
	}
	var redirects []string
	for _, r := range dig(t, cfg, "apps", "http", "servers", "nearhost_http", "routes").([]any) {
		if h := r.(map[string]any)["handle"].([]any)[0].(map[string]any); h["status_code"] == float64(308) {
			redirects = append(redirects, strs(dig(t, r, "match", 0, "host"))...)
		}
	}
	if !slices.Equal(redirects, []string{"localhost", "pro.test", "*.local.example.pro"}) {
		t.Errorf("http redirects = %v, want localhost among them", redirects)
	}
}

// With a web port, a server of its own serves the home page at localhost
// to loopback clients, even while a project serves localhost, and drops
// the rest. localhost is a subject either way.
func TestBuildConfigWebPortServesHomePage(t *testing.T) {
	raw, err := BuildConfig([]Site{
		{Alias: "a.test", Hosts: []string{"a.test", "localhost"}, Upstream: "127.0.0.1:1"},
	}, Options{StorageRoot: t.TempDir(), Version: "1.2.3", WebPort: 6327})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	web := dig(t, cfg, "apps", "http", "servers", "nearhost_web").(map[string]any)
	if got := strs(web["listen"]); !slices.Equal(got, []string{":6327"}) {
		t.Errorf("listen = %v, want :6327", got)
	}
	if got := strs(web["protocols"]); !slices.Equal(got, []string{"h1", "h2"}) {
		t.Errorf("protocols = %v, want h1 and h2, so no UDP port", got)
	}
	routes := web["routes"].([]any)
	if len(routes) != 2 || dig(t, routes[0], "handle", 0, "handler") != homeModule || dig(t, routes[0], "handle", 0, "web_port") != float64(6327) ||
		dig(t, routes[1], "handle", 0, "abort") != true {
		t.Errorf("routes = %v, want the home page, then a drop", routes)
	}
	if got := strs(dig(t, routes[0], "match", 0, "remote_ip", "ranges")); !slices.Equal(got, loopbackRanges) {
		t.Errorf("home route remote_ip = %v, want loopback", got)
	}
	if len(homeRoutes(t, cfg)) != 0 {
		t.Error("the https port serves the home page though a project serves localhost")
	}
	if got := tlsSubjects(cfg); !slices.Contains(got, "localhost") {
		t.Errorf("subjects = %v, want localhost", got)
	}
	// A config that does not listen has no web server, nor one without a
	// web port.
	raw, err = BuildConfig(nil, Options{StorageRoot: t.TempDir(), WebPort: 6327})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "nearhost_web") {
		t.Error("config with no listeners has a web server")
	}
	cfg = buildJSON(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
	if _, ok := dig(t, cfg, "apps", "http", "servers").(map[string]any)["nearhost_web"]; ok {
		t.Error("config without a web port has a web server")
	}
}

func TestBuildConfigHomePageVersionDefaultsToDev(t *testing.T) {
	cfg := buildJSON(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
	home := homeRoutes(t, cfg)
	if len(home) != 1 || dig(t, home[0], "handle", 0, "version") != "dev" {
		t.Errorf("home routes = %v, want one with version dev", home)
	}
}

// A project that serves localhost gets it, and the home page is not served
// at all, so no precedence has to decide.
func TestBuildConfigLeavesLocalhostToAProjectThatServesIt(t *testing.T) {
	cfg := buildJSON(t, []Site{
		{Alias: "a.test", Hosts: []string{"a.test", "localhost"}, Upstream: "127.0.0.1:1"},
	})
	if home := homeRoutes(t, cfg); len(home) != 0 {
		t.Errorf("home page routes = %v, want none", home)
	}
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes").([]any)
	if len(routes) != 3 {
		t.Fatalf("routes = %v, want a.test, localhost and the final abort", routes)
	}
	r := routes[1]
	if dig(t, r, "match", 0, "host", 0) != "localhost" || dig(t, r, "handle", 0, "alias") != "a.test" ||
		dig(t, proxyHandler(t, r), "upstreams", 0, "dial") != "127.0.0.1:1" {
		t.Errorf("localhost route = %v, want it proxied to a.test's upstream", r)
	}
}

// dockerRanges are the ranges a client range list of 172.16.0.0/12 and
// fd00:d0c::/64 comes out as: an IPv4 range is listed again in its
// IPv4-mapped form, as the loopback network is.
var dockerRanges = []string{"172.16.0.0/12", "::ffff:172.16.0.0/108", "fd00:d0c::/64"}

var dockerClients = []string{"172.16.0.0/12", "fd00:d0c::/64"}

// A site's extra client ranges are served on that site's routes alone,
// in the same match set as the host. Each host not served to them is
// followed by a drop for that host alone, so their clients cannot fall
// through to the opted-in site's pattern.
func TestBuildConfigAddsSiteClientsToItsRoutesOnly(t *testing.T) {
	cfg := build(t, []Site{
		{Alias: "a.test", Hosts: []string{"a.test", "*.a.local.x.io"}, Upstream: "127.0.0.1:1", Clients: dockerClients},
		{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2"},
	})
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes").([]any)
	seenA, seenB := 0, 0
	var dropped []string
	for i, r := range routes {
		m, ok := dig(t, r, "match").([]any)
		if !ok {
			continue
		}
		host := strs(dig(t, m[0], "host"))[0]
		if dig(t, m[0], "remote_ip") == nil {
			if dig(t, r, "handle", 0, "abort") != true || len(m[0].(map[string]any)) != 1 {
				t.Errorf("route %d = %v, want a drop matched by host alone", i, r)
			}
			if prev := strs(dig(t, routes[i-1], "match", 0, "host"))[0]; prev != host {
				t.Errorf("drop for %s follows the route for %s, want its own", host, prev)
			}
			dropped = append(dropped, host)
			continue
		}
		got := strs(dig(t, m[0], "remote_ip", "ranges"))
		switch host {
		case "a.test", "*.a.local.x.io":
			seenA++
			if want := append(slices.Clone(loopbackRanges), dockerRanges...); !slices.Equal(got, want) {
				t.Errorf("%s ranges = %v, want %v", host, got, want)
			}
		default:
			if host == "b.test" {
				seenB++
			}
			if !slices.Equal(got, loopbackRanges) {
				t.Errorf("%s ranges = %v, want loopback only", host, got)
			}
		}
	}
	if seenA != 2 || seenB != 1 {
		t.Errorf("saw %d routes of a and %d of b, want 2 and 1", seenA, seenB)
	}
	if want := []string{"b.test", "localhost"}; !slices.Equal(dropped, want) {
		t.Errorf("hosts followed by a drop = %v, want %v", dropped, want)
	}
}

func tlsPolicies(t *testing.T, cfg map[string]any) []any {
	t.Helper()
	return dig(t, cfg, "apps", "http", "servers", "nearhost", "tls_connection_policies").([]any)
}

// Handshakes from a site's extra ranges are accepted by a policy of their
// own, after the loopback one, matching the site's hosts as well, so a
// container cannot learn or reach a project that did not opt in.
func TestBuildConfigAddsADockerTLSPolicyMatchedByHost(t *testing.T) {
	a := Site{Alias: "a.test", Hosts: []string{"b.local.x.io", "a.test"}, Upstream: "127.0.0.1:1", Clients: dockerClients}
	b := Site{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2"}

	policies := tlsPolicies(t, build(t, []Site{a, b}))
	if len(policies) != 2 {
		t.Fatalf("policies = %v, want two", policies)
	}
	if got := strs(dig(t, policies[0], "match", "remote_ip", "ranges")); !slices.Equal(got, loopbackRanges) || len(policies[0].(map[string]any)["match"].(map[string]any)) != 1 {
		t.Errorf("first policy = %v, want the loopback one unchanged", policies[0])
	}
	match := dig(t, policies[1], "match").(map[string]any)
	if len(match) != 2 {
		t.Errorf("second policy match = %v, want remote_ip and nearhost_hosts only", match)
	}
	if got := strs(dig(t, match, "remote_ip", "ranges")); !slices.Equal(got, dockerRanges) {
		t.Errorf("second policy ranges = %v, want %v", got, dockerRanges)
	}
	if got := strs(dig(t, match, "nearhost_hosts", "hosts")); !slices.Equal(got, []string{"a.test", "b.local.x.io"}) {
		t.Errorf("second policy hosts = %v, want a's hosts, sorted", got)
	}
	if got := strs(dig(t, match, "nearhost_hosts", "except")); !slices.Equal(got, []string{"b.test", "localhost"}) {
		t.Errorf("second policy except = %v, want every other host, sorted", got)
	}

	if n := len(tlsPolicies(t, build(t, []Site{b}))); n != 1 {
		t.Errorf("%d policies with no site opted in, want 1", n)
	}

	same := Site{Alias: "c.test", Hosts: []string{"c.test"}, Upstream: "127.0.0.1:3", Clients: dockerClients}
	policies = tlsPolicies(t, build(t, []Site{a, b, same}))
	if len(policies) != 2 {
		t.Fatalf("policies = %v, want two for sites with the same clients", policies)
	}
	if got := strs(dig(t, policies[1], "match", "nearhost_hosts", "hosts")); !slices.Equal(got, []string{"a.test", "b.local.x.io", "c.test"}) {
		t.Errorf("shared policy hosts = %v", got)
	}

	other := Site{Alias: "d.test", Hosts: []string{"d.test"}, Upstream: "127.0.0.1:4", Clients: []string{"10.0.0.0/8"}}
	policies = tlsPolicies(t, build(t, []Site{other, a}))
	if len(policies) != 3 {
		t.Fatalf("policies = %v, want three for different clients", policies)
	}
	if got := strs(dig(t, policies[1], "match", "remote_ip", "ranges")); !slices.Equal(got, []string{"10.0.0.0/8", "::ffff:10.0.0.0/104"}) {
		t.Errorf("second policy ranges = %v, want the first site's", got)
	}
	if got := strs(dig(t, policies[2], "match", "remote_ip", "ranges")); !slices.Equal(got, dockerRanges) {
		t.Errorf("third policy ranges = %v, want the second site's", got)
	}
}

// The http port drops clients outside loopback and every site's extra
// ranges, redirects each host only to the clients it is served to, and
// follows each host not served to all of them with a drop for that host
// alone, ahead of the redirects Caddy adds.
func TestBuildConfigRedirectsSiteClientsFromHTTP(t *testing.T) {
	cfg := build(t, []Site{
		{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1", Clients: dockerClients},
		{Alias: "b.test", Hosts: []string{"b.test"}, Upstream: "127.0.0.1:2"},
		{Alias: "c.test", Hosts: []string{"c.test"}, Upstream: "127.0.0.1:3", Clients: []string{"172.16.0.0/12"}},
	})
	routes := dig(t, cfg, "apps", "http", "servers", "nearhost_http", "routes").([]any)
	union := append(slices.Clone(loopbackRanges), dockerRanges...)
	if got := strs(dig(t, routes[0], "match", 0, "not", 0, "remote_ip", "ranges")); !slices.Equal(got, union) {
		t.Errorf("first route ranges = %v, want %v", got, union)
	}
	want := map[string][]string{
		"a.test":    union,
		"b.test":    loopbackRanges,
		"c.test":    append(slices.Clone(loopbackRanges), "172.16.0.0/12", "::ffff:172.16.0.0/108"),
		"localhost": loopbackRanges,
	}
	var order []string
	for _, r := range routes[1 : len(routes)-1] {
		host := strs(dig(t, r, "match", 0, "host"))[0]
		if dig(t, r, "match", 0, "remote_ip") == nil {
			if dig(t, r, "handle", 0, "abort") != true {
				t.Errorf("host-only route for %s = %v, want a drop", host, r)
			}
			order = append(order, "drop "+host)
			continue
		}
		order = append(order, host)
		if got := strs(dig(t, r, "match", 0, "remote_ip", "ranges")); !slices.Equal(got, want[host]) {
			t.Errorf("redirect for %s ranges = %v, want %v", host, got, want[host])
		}
	}
	// a.test is served to the whole union, so nothing falls through it.
	wantOrder := []string{"a.test", "b.test", "drop b.test", "c.test", "drop c.test", "localhost", "drop localhost"}
	if !slices.Equal(order, wantOrder) {
		t.Errorf("routes = %v, want %v", order, wantOrder)
	}
	last := routes[len(routes)-1].(map[string]any)
	if h := dig(t, last, "handle", 0).(map[string]any); last["match"] != nil || h["abort"] != true {
		t.Errorf("last route = %v, want an abort", last)
	}
}

func TestBuildConfigRejectsBadClientRange(t *testing.T) {
	_, err := BuildConfig([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1", Clients: []string{"not-a-cidr"}}}, Options{StorageRoot: t.TempDir()})
	const want = `proxy: site a.test: client range "not-a-cidr" is not a CIDR`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

var updateGolden = flag.Bool("update", false, "rewrite the golden configs in testdata")

// With no site opted in to extra clients, the config is exactly what it
// was before sites could have them: no host-only drops, no Except. The
// golden file was written before the Docker client routes existed; rewrite
// it with -update only for a change meant to reach every config.
func TestBuildConfigWithoutClientsIsUnchanged(t *testing.T) {
	raw, err := BuildConfig([]Site{
		{Alias: "a.test", Hosts: []string{"a.test", "*.local.x.io", "internal.*.local.x.io"}, Upstream: "127.0.0.1:1"},
		{Alias: "b.test", Hosts: []string{"b.test", "api.local.x.io"}, Upstream: "127.0.0.1:2"},
	}, Options{StorageRoot: "/tmp/store", HTTPSPort: 8443, HTTPPort: 8080, Capture: true, Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "no-clients.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) {
		t.Errorf("config without clients changed:\n got %s\nwant %s", raw, want)
	}
}
