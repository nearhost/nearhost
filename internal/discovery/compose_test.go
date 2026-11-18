package discovery

import (
	"maps"
	"os"
	"slices"
	"strings"
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

func TestReadComposeNamesTheFile(t *testing.T) {
	files, err := ReadCompose(DirReader{Root: "testdata/compose-literal"})
	if err != nil || len(files) != 1 || files[0].Name != "compose.yaml" || len(files[0].Data) == 0 {
		t.Fatalf("compose.yaml not found: files=%+v err=%v", files, err)
	}
	files, err = ReadCompose(DirReader{Root: "testdata/empty"})
	if err != nil || files != nil {
		t.Fatalf("expected not found, got files=%+v err=%v", files, err)
	}
}

// composeNames lists the names of files.
func composeNames(files []ComposeFile) string {
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	return strings.Join(names, ",")
}

// Compose prefers compose.yaml, its canonical name, over the older
// docker-compose names, and reads the first override file beside it.
func TestReadComposeFollowsComposesOrder(t *testing.T) {
	const svc = "services: {}\n"
	cases := []struct {
		files memReader
		want  string
	}{
		{memReader{"compose.yaml": svc, "docker-compose.yml": svc}, "compose.yaml"},
		{memReader{"compose.yml": svc, "docker-compose.yml": svc}, "compose.yml"},
		{memReader{"docker-compose.yml": svc, "docker-compose.yaml": svc}, "docker-compose.yml"},
		{memReader{"docker-compose.yaml": svc}, "docker-compose.yaml"},
		{memReader{"compose.yaml": svc, "compose.override.yaml": svc}, "compose.yaml,compose.override.yaml"},
		{memReader{"compose.yaml": svc, "compose.override.yml": svc, "compose.override.yaml": svc}, "compose.yaml,compose.override.yml"},
		{memReader{"docker-compose.yml": svc, "docker-compose.override.yml": svc}, "docker-compose.yml,docker-compose.override.yml"},
		// compose-go takes the first override present whatever the
		// Compose file's name.
		{memReader{"compose.yaml": svc, "docker-compose.override.yml": svc}, "compose.yaml,docker-compose.override.yml"},
		// An override with no Compose file beside it is not read.
		{memReader{"compose.override.yaml": svc}, ""},
	}
	for _, c := range cases {
		files, err := ReadCompose(c.files)
		if err != nil {
			t.Fatal(err)
		}
		if got := composeNames(files); got != c.want {
			t.Errorf("files %v: read %q, want %q", slices.Sorted(maps.Keys(c.files)), got, c.want)
		}
	}
}

// An override's ports add to the service's, and a port both files publish
// is listed once.
func TestParseComposeFilesMergesTheOverride(t *testing.T) {
	files, err := ReadCompose(DirReader{Root: "testdata/compose-override"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseComposeFiles(files, nil)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b ComposePort) int { return a.Published - b.Published })
	want := []ComposePort{{Service: "web", Published: 8080, Target: 80}, {Service: "db", Published: 5433, Target: 5432}}
	slices.SortFunc(want, func(a, b ComposePort) int { return a.Published - b.Published })
	if !slices.Equal(got, want) {
		t.Errorf("ports = %+v, want %+v", got, want)
	}
}

// A parse error names the file that holds it.
func TestParseComposeFilesNamesTheBadFile(t *testing.T) {
	files := []ComposeFile{{Name: "compose.yaml", Data: []byte("services: {}\n")}, {Name: "compose.override.yaml", Data: []byte("services: [\n")}}
	if _, err := ParseComposeFiles(files, nil); err == nil || !strings.HasPrefix(err.Error(), "compose.override.yaml: ") {
		t.Fatalf("err = %v", err)
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

func TestParseComposeSkipsTargetRanges(t *testing.T) {
	data := []byte("services:\n  app:\n    ports:\n      - '8080:80-81'\n      - target: 90-91\n        published: 9090\n      - '7000:70'\n")
	ports, err := ParseCompose(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].Published != 7000 || ports[0].Target != 70 {
		t.Fatalf("ranged targets should be skipped, got %+v", ports)
	}
}

func TestParseComposeShortSyntaxWithIPv6HostAddress(t *testing.T) {
	data := []byte("services:\n  ws:\n    ports:\n      - '[::1]:6001:6002'\n      - '[::]:${WS_PORT:-7001}:7002/tcp'\n")
	ports, err := ParseCompose(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := portsByPublished(ports)
	if len(ports) != 2 || m[6001].Target != 6002 || m[7001].Target != 7002 || m[7001].Var != "WS_PORT" {
		t.Fatalf("IPv6 host addresses should keep their ports, got %+v", ports)
	}
}
