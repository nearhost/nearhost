package proxy

import (
	"encoding/json"
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
	if len(subjects) != 2 || subjects[1] != "b.test" {
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
	if got := routeHosts(cfg); !slices.Equal(got, []string{"a.test", "api.example.io"}) {
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
	if len(routes) != 3 {
		t.Fatalf("got %d routes, want one per site and a final catch-all", len(routes))
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
	last := routes[2].(map[string]any)
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
	// One Caddy route per host, then the final abort route.
	want := []string{"auth.local.redactr.pro", "platform.test", "pro.test", "internal.*.local.redactr.pro", "*.local.redactr.pro"}
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
	if got := routeHosts(cfg); !slices.Equal(got, []string{"a.test"}) {
		t.Fatalf("route hosts = %v", got)
	}
	if got := tlsSubjects(cfg); !slices.Equal(got, []string{"a.test"}) {
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
		if len(handle) != 3 || dig(t, handle[0], "handler") != tagModule || dig(t, handle[0], "alias") != "a.test" ||
			dig(t, handle[1], "handler") != captureModule || dig(t, handle[2], "handler") != "reverse_proxy" {
			t.Errorf("route %d handlers = %v, want tag, capture, reverse_proxy", i, handle)
		}
	}
}
