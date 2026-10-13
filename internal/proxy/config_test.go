package proxy

import (
	"encoding/json"
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
		{Alias: "a.test", Upstream: "127.0.0.1:8080"},
		{Alias: "b.test", Upstream: "127.0.0.1:5000"},
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
	handler := dig(t, srv, "routes", 0, "handle", 0)
	if dig(t, handler, "handler") != "reverse_proxy" || dig(t, handler, "upstreams", 0, "dial") != "127.0.0.1:8080" {
		t.Errorf("reverse proxy wrong: %v", handler)
	}
	if dig(t, handler, "flush_interval") != float64(-1) {
		t.Error("flush_interval should be -1 for streaming")
	}
	if dig(t, srv, "routes", 1, "match", 0, "host", 0) != "b.test" {
		t.Error("second route missing")
	}
	if dig(t, srv, "logs", "logger_names", "a.test", 0) != "a.test" {
		t.Error("per-site logger name missing")
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
	siteLog := dig(t, cfg, "logging", "logs", "a.test")
	if dig(t, siteLog, "writer", "output") != "nearhost" || dig(t, siteLog, "writer", "alias") != "a.test" || dig(t, siteLog, "include", 0) != "http.log.access.a.test" {
		t.Errorf("site log wrong: %v", siteLog)
	}
	def := dig(t, cfg, "logging", "logs", "default")
	if dig(t, def, "writer", "output") != "nearhost" || dig(t, def, "exclude", 0) != "http.log.access" {
		t.Errorf("default log wrong: %v", def)
	}
}

func TestBuildConfigDefaultsPorts(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/tmp/store"})
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
	if _, err := BuildConfig([]Site{{Alias: "a.test", Upstream: ""}}, Options{StorageRoot: "/x"}); err == nil {
		t.Error("empty upstream should fail")
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
	cfg := build(t, []Site{{Alias: "a.test", Upstream: "127.0.0.1:8080"}})
	h := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes", 0, "handle", 0)
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
	cfg := build(t, []Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}})
	srv := dig(t, cfg, "apps", "http", "servers", "nearhost").(map[string]any)
	if a, ok := srv["automatic_https"]; ok {
		t.Errorf("automatic_https must stay default so port 80 redirects: %v", a)
	}
	if l := srv["listen"].([]any); len(l) != 1 || l[0] != ":443" {
		t.Errorf("listen = %v, want only :443", l)
	}
}

func TestBuildConfigIsDeterministicAndSorted(t *testing.T) {
	a := []Site{{Alias: "b.test", Upstream: "127.0.0.1:2"}, {Alias: "a.test", Upstream: "127.0.0.1:1"}}
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

func TestBuildConfigRejectsDuplicateAliases(t *testing.T) {
	_, err := BuildConfig([]Site{{Alias: "a.test", Upstream: "x:1"}, {Alias: "a.test", Upstream: "x:2"}}, Options{StorageRoot: "/s"})
	if err == nil {
		t.Error("duplicate alias should fail")
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
	sites := []Site{{"a.test", "127.0.0.1:1"}, {"B.test", "127.0.0.1:2"}, {"A.TEST", "127.0.0.1:3"}}
	if _, err := BuildConfig(sites, Options{StorageRoot: "/x"}); err == nil {
		t.Error("aliases naming the same host should fail")
	}
}

func TestBuildConfigKeepsUpgradedStreamsAcrossReloads(t *testing.T) {
	raw, err := BuildConfig([]Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}}, Options{StorageRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	handler := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes", 0, "handle", 0)
	if dig(t, handler, "stream_close_delay") != "24h" {
		t.Errorf("stream_close_delay = %v, want 24h so WebSockets survive a reload", dig(t, handler, "stream_close_delay"))
	}
}

func TestBuildConfigDisablesStorageClean(t *testing.T) {
	for _, sites := range [][]Site{nil, {{Alias: "a.test", Upstream: "127.0.0.1:1"}}} {
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
	cfg := build(t, []Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}, {Alias: "b.test", Upstream: "127.0.0.1:2"}})
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
