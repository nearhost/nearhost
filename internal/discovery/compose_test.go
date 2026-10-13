package discovery

import (
	"os"
	"testing"
)

func portsByPublished(ps []ComposePort) map[int]ComposePort {
	m := map[int]ComposePort{}
	for _, p := range ps {
		m[p.Published] = p
	}
	return m
}

func TestParseComposeResolvesSailVariables(t *testing.T) {
	data, _ := os.ReadFile("testdata/sail-custom/docker-compose.yml")
	env := map[string]string{"APP_PORT": "8080", "VITE_PORT": "5174", "FORWARD_DB_PORT": "3307"}
	ports, err := ParseCompose(data, env)
	if err != nil {
		t.Fatal(err)
	}
	m := portsByPublished(ports)
	cases := []struct {
		published, target int
		service, v        string
	}{
		{8080, 80, "laravel.test", "APP_PORT"},
		{5174, 5174, "laravel.test", "VITE_PORT"},
		{3307, 3306, "mysql", "FORWARD_DB_PORT"},
		{6379, 6379, "redis", "FORWARD_REDIS_PORT"},
		{1025, 1025, "mailpit", "FORWARD_MAILPIT_PORT"},
		{8025, 8025, "mailpit", "FORWARD_MAILPIT_DASHBOARD_PORT"},
	}
	for _, c := range cases {
		got, ok := m[c.published]
		if !ok || got.Target != c.target || got.Service != c.service || got.Var != c.v {
			t.Errorf("port %d: got %+v ok=%v, want target=%d service=%s var=%s", c.published, got, ok, c.target, c.service, c.v)
		}
	}
	if len(ports) != len(cases) {
		t.Fatalf("expected %d ports, got %d: %+v", len(cases), len(ports), ports)
	}
}

func TestParseComposeUsesDefaultsWhenEnvMissing(t *testing.T) {
	data, _ := os.ReadFile("testdata/sail-defaults/docker-compose.yml")
	ports, err := ParseCompose(data, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	m := portsByPublished(ports)
	if m[80].Var != "APP_PORT" || m[5173].Var != "VITE_PORT" || m[3306].Target != 3306 {
		t.Fatalf("defaults not applied: %+v", ports)
	}
}

func TestParseComposeHandlesLiteralLongSyntaxAndSkips(t *testing.T) {
	data, _ := os.ReadFile("testdata/compose-literal/compose.yaml")
	ports, err := ParseCompose(data, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	m := portsByPublished(ports)
	if m[8080].Target != 80 || m[8080].Service != "web" {
		t.Errorf("literal mapping wrong: %+v", m[8080])
	}
	if m[9000].Target != 9000 {
		t.Errorf("same-port mapping wrong: %+v", m[9000])
	}
	if m[8443].Target != 443 || m[8443].Service != "api" {
		t.Errorf("long syntax wrong: %+v", m[8443])
	}
	if _, udp := m[5353]; udp {
		t.Error("udp port should be skipped")
	}
	if _, unresolved := m[0]; unresolved {
		t.Error("unresolved variable should be skipped")
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 ports, got %+v", ports)
	}
}

func TestParseComposeRejectsInvalidYAML(t *testing.T) {
	if _, err := ParseCompose([]byte("services: [oops"), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestReadComposeFindsAnyStandardName(t *testing.T) {
	data, ok, err := readCompose(DirReader{Root: "testdata/compose-literal"})
	if err != nil || !ok || len(data) == 0 {
		t.Fatalf("compose.yaml not found: ok=%v err=%v", ok, err)
	}
	_, ok, err = readCompose(DirReader{Root: "testdata/empty"})
	if err != nil || ok {
		t.Fatalf("expected not found, got ok=%v err=%v", ok, err)
	}
}

func TestIsSail(t *testing.T) {
	sail, _ := os.ReadFile("testdata/sail-custom/docker-compose.yml")
	plain, _ := os.ReadFile("testdata/compose-literal/compose.yaml")
	if !IsSail(sail) || IsSail(plain) {
		t.Fatal("IsSail misclassified")
	}
}

func TestParseComposeLongSyntaxKeepsVariableName(t *testing.T) {
	data := []byte("services:\n  web:\n    ports:\n      - target: 80\n        published: \"${APP_PORT}\"\n")
	ports, err := ParseCompose(data, map[string]string{"APP_PORT": " 8080 "})
	if err != nil || len(ports) != 1 || ports[0].Published != 8080 || ports[0].Var != "APP_PORT" {
		t.Fatalf("got %+v err=%v", ports, err)
	}
}

func TestParseComposeDashDefaultOnlyWhenUnset(t *testing.T) {
	data := []byte("services:\n  a:\n    ports:\n      - \"${P-80}:80\"\n  b:\n    ports:\n      - \"${Q:-81}:81\"\n")
	ports, err := ParseCompose(data, map[string]string{"P": "", "Q": ""})
	if err != nil {
		t.Fatal(err)
	}
	m := portsByPublished(ports)
	if len(ports) != 1 || m[81].Service != "b" {
		t.Fatalf("got %+v", ports)
	}
	ports, _ = ParseCompose(data, map[string]string{})
	if len(ports) != 2 {
		t.Fatalf("unset should use both defaults: %+v", ports)
	}
}
