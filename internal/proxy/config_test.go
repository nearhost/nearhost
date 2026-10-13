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

func TestBuildConfigDefaultsPortsAndAllowsNoSites(t *testing.T) {
	raw, err := BuildConfig(nil, Options{StorageRoot: "/tmp/store"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	if dig(t, cfg, "apps", "http", "https_port") != float64(443) || dig(t, cfg, "apps", "http", "http_port") != float64(80) {
		t.Error("default ports wrong")
	}
	if routes := dig(t, cfg, "apps", "http", "servers", "nearhost", "routes").([]any); len(routes) != 0 {
		t.Errorf("expected no routes, got %v", routes)
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
	cfg := build(t, nil)
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
