package discovery

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func routeMap(res *Result) map[int]Candidate {
	m := map[int]Candidate{}
	for _, c := range res.Routes {
		m[c.Port] = c
	}
	return m
}

func TestScanVite(t *testing.T) {
	info := ScanVite([]byte(`export default defineConfig({ server: { port: 3000, https: true, allowedHosts: ['a.test', "b.test"] } })`))
	if !info.Found || info.Port != 3000 || !info.HTTPS || len(info.AllowedHosts) != 2 || info.AllowedHosts[1] != "b.test" {
		t.Fatalf("unexpected: %+v", info)
	}
	all := ScanVite([]byte(`server: { allowedHosts: true }`))
	if !all.AllowAllHosts {
		t.Fatalf("expected AllowAllHosts, got %+v", all)
	}
	none := ScanVite([]byte(`export default defineConfig({ plugins: [laravel()] })`))
	if none.Port != 0 || none.HTTPS || none.AllowAllHosts || len(none.AllowedHosts) != 0 {
		t.Fatalf("expected empty info, got %+v", none)
	}
}

func TestDiscoverSailCustom(t *testing.T) {
	res, err := Discover(DirReader{Root: "testdata/sail-custom"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasLaravel || !res.HasVite || !res.Sail {
		t.Fatalf("flags wrong: %+v", res)
	}
	m := routeMap(res)
	if m[8080].Role != config.RoleWeb || m[8080].Label != "Laravel (APP_PORT)" {
		t.Errorf("web route wrong: %+v", m[8080])
	}
	if m[5174].Role != config.RoleLoopback || m[5174].Label != "Vite (VITE_PORT)" {
		t.Errorf("vite route wrong: %+v", m[5174])
	}
	if m[3307].Role != config.RoleIgnore || m[3307].Label != "Database (FORWARD_DB_PORT)" {
		t.Errorf("db route wrong: %+v", m[3307])
	}
	if m[6379].Role != config.RoleIgnore || m[8025].Label != "Mailpit dashboard (FORWARD_MAILPIT_DASHBOARD_PORT)" {
		t.Errorf("other routes wrong: %+v", res.Routes)
	}
	for i := 1; i < len(res.Routes); i++ {
		if res.Routes[i-1].Port > res.Routes[i].Port {
			t.Fatal("routes should be sorted by port")
		}
	}
}

func TestDiscoverSailDefaults(t *testing.T) {
	res, err := Discover(DirReader{Root: "testdata/sail-defaults"})
	if err != nil {
		t.Fatal(err)
	}
	m := routeMap(res)
	if m[80].Role != config.RoleWeb || m[5173].Role != config.RoleLoopback {
		t.Fatalf("defaults wrong: %+v", res.Routes)
	}
}

func TestDiscoverPlainVite(t *testing.T) {
	res, err := Discover(DirReader{Root: "testdata/vite-plain"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HasLaravel || !res.HasVite || res.Sail {
		t.Fatalf("flags wrong: %+v", res)
	}
	m := routeMap(res)
	if len(res.Routes) != 1 || m[3000].Role != config.RoleWeb || m[3000].Source != "vite" {
		t.Fatalf("expected single web route on 3000, got %+v", res.Routes)
	}
}

func TestDiscoverComposeLiteral(t *testing.T) {
	res, err := Discover(DirReader{Root: "testdata/compose-literal"})
	if err != nil {
		t.Fatal(err)
	}
	m := routeMap(res)
	if m[8080].Role != config.RoleWeb || m[8080].Label != "web" {
		t.Errorf("port 80 target should become web: %+v", m[8080])
	}
	if m[9000].Role != config.RoleIgnore || m[8443].Role != config.RoleIgnore {
		t.Errorf("others should be ignore: %+v", res.Routes)
	}
}

func TestDiscoverEmptyProject(t *testing.T) {
	res, err := Discover(DirReader{Root: "testdata/empty"})
	if err != nil || len(res.Routes) != 0 || res.HasVite || res.HasLaravel {
		t.Fatalf("expected empty result, got %+v %v", res, err)
	}
}

func TestDiscoverSingleUnknownPortBecomesWeb(t *testing.T) {
	res, err := Discover(memReader{"compose.yaml": "services:\n  app:\n    ports:\n      - '4000:4000'\n"})
	if err != nil || len(res.Routes) != 1 || res.Routes[0].Role != config.RoleWeb {
		t.Fatalf("expected lone port to be web: %+v %v", res, err)
	}
}

func TestConfigRoutes(t *testing.T) {
	res, _ := Discover(DirReader{Root: "testdata/sail-custom"})
	routes := res.ConfigRoutes()
	if len(routes) != len(res.Routes) || routes[0].Port != res.Routes[0].Port || routes[0].Label == "" {
		t.Fatalf("ConfigRoutes mismatch: %+v", routes)
	}
}

func codes(advs []Advisory) string {
	var cs []string
	for _, a := range advs {
		cs = append(cs, a.Code)
	}
	return strings.Join(cs, ",")
}

func TestAdviseSailCustomAppURL(t *testing.T) {
	res, _ := Discover(DirReader{Root: "testdata/sail-custom"})
	advs := Advise(res, "myapp.test", nil)
	if codes(advs) != "app-url" {
		t.Fatalf("expected only app-url, got %s", codes(advs))
	}
	if advs[0].Snippet != "APP_URL=https://myapp.test" {
		t.Fatalf("snippet: %q", advs[0].Snippet)
	}
}

func TestAdviseLoopbackClash(t *testing.T) {
	res, _ := Discover(DirReader{Root: "testdata/sail-custom"})
	others := []config.Project{{Name: "other", Enabled: true, Routes: []config.Route{{Port: 5174, Role: config.RoleLoopback}}}}
	advs := Advise(res, "myapp.test", others)
	if !strings.Contains(codes(advs), "loopback-clash") {
		t.Fatalf("expected clash, got %s", codes(advs))
	}
	for _, a := range advs {
		if a.Code == "loopback-clash" {
			if !strings.Contains(a.Message, "other") || a.Snippet != "VITE_PORT=5173" {
				t.Fatalf("clash advisory wrong: %+v", a)
			}
		}
	}
	others[0].Enabled = false
	if strings.Contains(codes(Advise(res, "myapp.test", others)), "loopback-clash") {
		t.Fatal("disabled projects should not clash")
	}
}

func TestAdviseViteDefaultPort(t *testing.T) {
	res, _ := Discover(DirReader{Root: "testdata/sail-defaults"})
	advs := Advise(res, "defaults.test", nil)
	if !strings.Contains(codes(advs), "vite-default-port") {
		t.Fatalf("expected vite-default-port, got %s", codes(advs))
	}
}

func TestAdviseAllowedHosts(t *testing.T) {
	res, _ := Discover(DirReader{Root: "testdata/vite-plain"})
	advs := Advise(res, "bar.test", nil)
	if codes(advs) != "allowed-hosts" || !strings.Contains(advs[0].Snippet, "allowedHosts: ['bar.test']") {
		t.Fatalf("expected allowed-hosts advisory, got %+v", advs)
	}
	if len(Advise(res, "foo.test", nil)) != 0 {
		t.Fatal("alias already allowed should produce no advisory")
	}
}

func TestAdviseAllowedHostsHonoursWildcardSuffix(t *testing.T) {
	res := &Result{HasVite: true, Vite: ViteInfo{Found: true, Port: 3000, AllowedHosts: []string{".test"}}}
	if len(Advise(res, "any.test", nil)) != 0 {
		t.Fatal("leading-dot entry should allow subdomains")
	}
}

func TestDiscoverDeterministicWithDuplicatePorts(t *testing.T) {
	compose := "services:\n"
	for _, s := range []string{"zeta", "alpha", "mid", "beta", "omega"} {
		compose += "  " + s + ":\n    ports:\n      - '9000:9000'\n      - '9001:9001'\n"
	}
	first, err := Discover(memReader{"compose.yaml": compose})
	if err != nil {
		t.Fatal(err)
	}
	if got := routeMap(first)[9000].Label; got != "alpha" {
		t.Fatalf("expected lowest service name to win, got %q", got)
	}
	for i := 0; i < 50; i++ {
		again, err := Discover(memReader{"compose.yaml": compose})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first.Routes, again.Routes) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first.Routes, again.Routes)
		}
	}
}

func TestAdviseAppURLTrimsWhitespace(t *testing.T) {
	res := &Result{HasLaravel: true, Env: map[string]string{"APP_URL": " https://myapp.test "}}
	if len(Advise(res, "myapp.test", nil)) != 0 {
		t.Fatal("surrounding whitespace in APP_URL should not trigger an advisory")
	}
}

func TestDiscoverLoneLoopbackRouteStaysLoopback(t *testing.T) {
	res, err := Discover(memReader{"compose.yaml": "services:\n  vite:\n    ports:\n      - '${VITE_PORT:-5173}:5173'\n"})
	if err != nil || len(res.Routes) != 1 || res.Routes[0].Role != config.RoleLoopback {
		t.Fatalf("lone Vite port must not become web: %+v %v", res, err)
	}
}

func TestAdviseAppURLMessageUsesTrimmedValue(t *testing.T) {
	res := &Result{HasLaravel: true, Env: map[string]string{"APP_URL": " http://x.test "}}
	advs := Advise(res, "myapp.test", nil)
	if len(advs) != 1 || !strings.Contains(advs[0].Message, `"http://x.test"`) {
		t.Fatalf("unexpected: %+v", advs)
	}
}

func TestAdviseViteDefaultPortPlainVite(t *testing.T) {
	res, err := Discover(memReader{
		"package.json":   `{"devDependencies":{"vite":"^5"}}`,
		"vite.config.ts": "export default defineConfig({ server: { allowedHosts: ['x.test'] } })",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codes(Advise(res, "x.test", nil)), "vite-default-port") {
		t.Fatalf("plain Vite without a port should get vite-default-port, got %s", codes(Advise(res, "x.test", nil)))
	}
}

func TestAdviseNoViteDefaultPortWhenConfigSetsPort(t *testing.T) {
	res, err := Discover(memReader{
		"docker-compose.yml": "services:\n  laravel.test:\n    image: sail-8.3/app\n    ports:\n      - '${APP_PORT:-80}:80'\n",
		"package.json":       `{"devDependencies":{"vite":"^5"}}`,
		"vite.config.js":     "export default { server: { port: 5199 } }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sail {
		t.Fatal("fixture should be Sail")
	}
	if strings.Contains(codes(Advise(res, "x.test", nil)), "vite-default-port") {
		t.Fatal("server.port is set, so no vite-default-port advisory")
	}
}

func TestAdviseViteDefaultPortSnippetMatchesProjectKind(t *testing.T) {
	plain, err := Discover(memReader{
		"package.json":   `{"devDependencies":{"vite":"^5"}}`,
		"vite.config.ts": "export default defineConfig({})",
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range Advise(plain, "x.test", nil) {
		if a.Code != "vite-default-port" {
			continue
		}
		found = true
		if !strings.Contains(a.Snippet, "port: 5173") || strings.Contains(a.Snippet, "VITE_PORT") || strings.Contains(a.Message, "VITE_PORT") {
			t.Fatalf("plain Vite advisory should name server.port only: %+v", a)
		}
	}
	if !found {
		t.Fatal("expected vite-default-port advisory")
	}

	sail, _ := Discover(DirReader{Root: "testdata/sail-defaults"})
	for _, a := range Advise(sail, "defaults.test", nil) {
		if a.Code == "vite-default-port" {
			if a.Snippet != "VITE_PORT=5174" || strings.Contains(a.Message, "server.port") {
				t.Fatalf("Sail advisory should keep VITE_PORT only: %+v", a)
			}
			return
		}
	}
	t.Fatal("expected Sail vite-default-port advisory")
}

func TestScanViteReadsOnlyServerBlock(t *testing.T) {
	cases := []struct {
		name, src string
		port      int
		allowAll  bool
	}{
		{"preview only", `export default defineConfig({ preview: { port: 4173, allowedHosts: true } })`, 0, false},
		{"preview before server", `export default { preview: { port: 4173 }, server: { port: 3000 } }`, 3000, false},
		{"hmr port only", `export default { server: { hmr: { port: 24678 } } }`, 0, false},
		{"nested blocks before port", `export default { server: { hmr: { port: 24678 }, proxy: { '/api': { target: 'http://x' } }, port: 3001, allowedHosts: true } }`, 3001, true},
		{"strictPort is not port", `export default { server: { strictPort: true } }`, 0, false},
	}
	for _, c := range cases {
		info := ScanVite([]byte(c.src))
		if info.Port != c.port || info.AllowAllHosts != c.allowAll {
			t.Errorf("%s: got port=%d allowAll=%v, want port=%d allowAll=%v", c.name, info.Port, info.AllowAllHosts, c.port, c.allowAll)
		}
	}
	hosts := ScanVite([]byte(`export default { preview: { allowedHosts: ['p.test'] }, server: { allowedHosts: ['s.test'] } }`))
	if len(hosts.AllowedHosts) != 1 || hosts.AllowedHosts[0] != "s.test" {
		t.Errorf("expected only server.allowedHosts, got %+v", hosts.AllowedHosts)
	}
}

func TestDiscoverVitePortMatchDoesNotPromoteLoopback(t *testing.T) {
	res, err := Discover(memReader{
		".env":           "VITE_PORT=5180\n",
		"vite.config.js": "export default { server: { port: 5180 } }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Routes) != 1 || res.Routes[0].Role != config.RoleLoopback {
		t.Fatalf("VITE_PORT loopback must not become web: %+v", res.Routes)
	}
}

func TestDiscoverLoneKnownServicePortIsNotWeb(t *testing.T) {
	res, err := Discover(memReader{"compose.yaml": "services:\n  mysql:\n    ports:\n      - '${FORWARD_DB_PORT:-3306}:3306'\n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Routes) != 1 || res.Routes[0].Role != config.RoleIgnore {
		t.Fatalf("lone database port must stay ignored: %+v", res.Routes)
	}
}

func TestAdviseViteDefaultPortIgnoresUnwiredEnvOutsideSail(t *testing.T) {
	files := memReader{
		".env":           "VITE_PORT=5180\n",
		"package.json":   `{"devDependencies":{"vite":"^5"}}`,
		"vite.config.ts": "export default defineConfig({})",
	}
	res, err := Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codes(Advise(res, "x.test", nil)), "vite-default-port") {
		t.Fatalf("VITE_PORT that the Vite config never reads should not hide the advisory, got %q", codes(Advise(res, "x.test", nil)))
	}

	files["vite.config.ts"] = "export default defineConfig({ server: { port: Number(process.env.VITE_PORT) } })"
	wired, err := Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(codes(Advise(wired, "x.test", nil)), "vite-default-port") {
		t.Fatal("VITE_PORT wired into the Vite config sets the port, so no advisory")
	}

	sail, err := Discover(memReader{
		".env":               "VITE_PORT=5180\n",
		"docker-compose.yml": "services:\n  laravel.test:\n    image: sail-8.3/app\n    ports:\n      - '${APP_PORT:-80}:80'\n      - '${VITE_PORT:-5173}:${VITE_PORT:-5173}'\n",
		"package.json":       `{"devDependencies":{"vite":"^5"}}`,
		"vite.config.js":     "export default defineConfig({})",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(codes(Advise(sail, "x.test", nil)), "vite-default-port") {
		t.Fatal("Sail honours VITE_PORT from .env, so no advisory")
	}
}
