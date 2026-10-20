package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
)

func routeMap(res *Result) map[int]Candidate {
	m := map[int]Candidate{}
	for _, c := range res.Routes {
		m[c.Port] = c
	}
	return m
}

// testPresets loads the named presets from testdata/presets.
func testPresets(t *testing.T, names ...string) []*preset.Preset {
	t.Helper()
	var out []*preset.Preset
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("testdata", "presets", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		p, err := preset.Parse(name, data)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// inline parses a preset written in the test.
func inline(t *testing.T, name, body string) *preset.Preset {
	t.Helper()
	p, err := preset.Parse(name, []byte("schema: 1\nname: "+name+"\ntitle: T\n"+body))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func discover(t *testing.T, r FileReader, opts Options) *Result {
	t.Helper()
	res, err := Discover(r, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Without presets the core reads .env and nothing else, even beside a
// Compose file.
func TestDiscoverWithoutPresetsReadsOnlyEnv(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{})
	var got []string
	for _, c := range res.Routes {
		got = append(got, fmt.Sprintf("%d %s %s %s", c.Port, c.Role, c.Label, c.Source))
	}
	want := "3307 ignore forward-db (FORWARD_DB_PORT) env:FORWARD_DB_PORT\n" +
		"5174 ignore vite (VITE_PORT) env:VITE_PORT\n" +
		"8080 ignore app (APP_PORT) env:APP_PORT"
	if strings.Join(got, "\n") != want {
		t.Fatalf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	if len(res.Applied) != 0 || len(res.Suggested) != 0 || len(res.Warnings) != 0 {
		t.Errorf("nothing should apply: %+v", res)
	}
}

// A preset with `compose: ports` brings back every published port.
func TestDiscoverComposePortsWithAPreset(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{Presets: testPresets(t, "compose")})
	m := routeMap(res)
	if len(res.Routes) != 6 {
		t.Fatalf("expected the six Compose ports, got %+v", res.Routes)
	}
	if m[8080].Role != config.RoleWeb || m[8080].Label != "laravel.test (APP_PORT)" || m[8080].Preset != "" || m[8080].Source != "compose:laravel.test" {
		t.Errorf("container port 80 should be web with a plain label: %+v", m[8080])
	}
	if m[5174].Role != config.RoleIgnore || m[5174].Label != "laravel.test (VITE_PORT)" {
		t.Errorf("nothing but a preset makes a loopback route: %+v", m[5174])
	}
	if m[6379].Label != "redis (FORWARD_REDIS_PORT)" {
		t.Errorf("label = %q", m[6379].Label)
	}
	if strings.Join(res.Applied, ",") != "compose" || len(res.Warnings) != 0 {
		t.Errorf("Applied = %v, Warnings = %v", res.Applied, res.Warnings)
	}
	for i := 1; i < len(res.Routes); i++ {
		if res.Routes[i-1].Port > res.Routes[i].Port {
			t.Fatal("routes should be sorted by port")
		}
	}
}

func TestDiscoverComposeOnlyProjectNeedsAPreset(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/compose-literal"}, Options{})
	if len(res.Routes) != 0 {
		t.Fatalf("no preset asked for Compose ports: %+v", res.Routes)
	}
	res = discover(t, DirReader{Root: "testdata/compose-literal"}, Options{Presets: testPresets(t, "compose")})
	m := routeMap(res)
	if len(res.Routes) != 3 || m[8080].Role != config.RoleWeb || m[8080].Label != "web" {
		t.Errorf("port 80 target should become web: %+v", res.Routes)
	}
	if m[9000].Role != config.RoleIgnore || m[8443].Role != config.RoleIgnore {
		t.Errorf("others should be ignore: %+v", res.Routes)
	}
}

// A preset that is enabled but does not match asks for nothing.
func TestDiscoverComposeNeedsTheAskingPresetActive(t *testing.T) {
	p := inline(t, "x", "detect: 'false'\ncompose: ports\n")
	res := discover(t, DirReader{Root: "testdata/compose-literal"}, Options{Presets: []*preset.Preset{p}})
	if len(res.Routes) != 0 {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

// An unparsable Compose file fails discovery only when a preset asks for it.
func TestDiscoverBadComposeFailsOnlyWhenAskedFor(t *testing.T) {
	r := memReader{"compose.yaml": "services: [", ".env": "APP_PORT=8080\n"}
	if res, err := Discover(r, Options{}); err != nil || len(res.Routes) != 1 {
		t.Fatalf("without a preset the Compose file is not parsed: %+v %v", res, err)
	}
	if _, err := Discover(r, Options{Presets: testPresets(t, "compose")}); err == nil {
		t.Fatal("expected the Compose parse error")
	}
}

// A port 80 that is not a Compose candidate has no container port, so the
// Compose rule does not make it web.
func TestDiscoverEnvPort80IsNotWebOnItsOwn(t *testing.T) {
	res := discover(t, memReader{".env": "APP_PORT=80\nDB_PORT=3306\n"}, Options{})
	if m := routeMap(res); len(res.Routes) != 2 || m[80].Role != config.RoleIgnore || m[3306].Role != config.RoleIgnore {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

func TestDiscoverEmptyProject(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/empty"}, Options{Presets: testPresets(t, "stack", "bundler")})
	if len(res.Routes) != 0 || len(res.Applied) != 0 {
		t.Fatalf("expected an empty result, got %+v", res)
	}
}

func TestDiscoverSingleUnknownPortBecomesWeb(t *testing.T) {
	res := discover(t, memReader{"compose.yaml": "services:\n  app:\n    ports:\n      - '4000:4000'\n"}, Options{Presets: testPresets(t, "compose")})
	if len(res.Routes) != 1 || res.Routes[0].Role != config.RoleWeb {
		t.Fatalf("expected the lone port to be web: %+v", res.Routes)
	}
}

func TestDiscoverPublishedPort80IsWebWhenTheTargetIsUnknown(t *testing.T) {
	res := discover(t, memReader{"compose.yaml": "services:\n  a:\n    ports:\n      - published: 80\n  b:\n    ports:\n      - '9000:9000'\n"}, Options{Presets: testPresets(t, "compose")})
	if m := routeMap(res); m[80].Role != config.RoleWeb || m[9000].Role != config.RoleIgnore {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

func TestDiscoverDeterministicWithDuplicatePorts(t *testing.T) {
	r := memReader{"compose.yaml": "services:\n  b:\n    ports:\n      - '8080:80'\n  a:\n    ports:\n      - '8080:8080'\n"}
	for i := 0; i < 20; i++ {
		res := discover(t, r, Options{Presets: testPresets(t, "compose")})
		if len(res.Routes) != 1 || res.Routes[0].Label != "a" {
			t.Fatalf("run %d: routes = %+v, want the one from service a", i, res.Routes)
		}
	}
}

func TestDiscoverAppliesPresetPorts(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{Presets: testPresets(t, "stack", "bundler")})
	if got := strings.Join(res.Applied, ","); got != "bundler,stack" {
		t.Fatalf("Applied = %s, want bundler,stack", got)
	}
	m := routeMap(res)
	if m[8080].Role != config.RoleWeb || m[8080].Label != "App (APP_PORT)" || m[8080].Preset != "stack" {
		t.Errorf("web route wrong: %+v", m[8080])
	}
	if m[5174].Role != config.RoleLoopback || m[5174].Label != "Assets (VITE_PORT)" {
		t.Errorf("loopback route wrong: %+v", m[5174])
	}
	if m[3307].Role != config.RoleIgnore || m[3307].Label != "Database (FORWARD_DB_PORT)" {
		t.Errorf("database route wrong: %+v", m[3307])
	}
	if m[6379].Label != "redis (FORWARD_REDIS_PORT)" || m[6379].Preset != "" {
		t.Errorf("a variable no preset knows keeps its plain label: %+v", m[6379])
	}
	if len(res.Routes) != 6 || len(res.Warnings) != 0 {
		t.Errorf("routes = %+v, warnings = %v", res.Routes, res.Warnings)
	}
}

func TestDiscoverUsesComposeDefaultsWhenEnvIsSilent(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-defaults"}, Options{Presets: testPresets(t, "stack", "bundler")})
	m := routeMap(res)
	if m[80].Role != config.RoleWeb || m[5173].Role != config.RoleLoopback || m[3306].Label != "Database (FORWARD_DB_PORT)" {
		t.Fatalf("defaults wrong: %+v", res.Routes)
	}
}

// A preset variable set in .env becomes a route when Compose publishes
// nothing through it.
func TestDiscoverAddsPresetPortsFromEnv(t *testing.T) {
	p := inline(t, "x", "detect: 'true'\nports:\n  - {var: APP_PORT, label: App, web: preferred}\n  - {var: ASSET_PORT, label: Assets, role: loopback}\n  - {var: WORDS, label: Words}\n  - {var: HUGE, label: Huge}\n")
	res := discover(t, memReader{".env": "APP_PORT= 3000 \nASSET_PORT=5174\nWORDS=eighty\nHUGE=70000\n"}, Options{Presets: []*preset.Preset{p}})
	m := routeMap(res)
	if len(res.Routes) != 2 || m[3000].Role != config.RoleWeb || m[3000].Label != "App (APP_PORT)" || m[3000].Source != "env:APP_PORT" || m[5174].Role != config.RoleLoopback {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

func TestDiscoverPresetRouteFromFacts(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/vite-plain"}, Options{Presets: testPresets(t, "stack", "bundler")})
	if got := strings.Join(res.Applied, ","); got != "bundler" {
		t.Fatalf("Applied = %s, want only bundler", got)
	}
	if len(res.Routes) != 1 {
		t.Fatalf("routes = %+v", res.Routes)
	}
	c := res.Routes[0]
	if c.Port != 3000 || c.Role != config.RoleWeb || c.Label != "Bundler (config)" || c.Source != "preset:bundler" || c.Preset != "bundler" {
		t.Fatalf("route = %+v", c)
	}
}

// A port Compose already publishes keeps its label when a preset names the
// same port, and takes the preset's web hint.
func TestDiscoverPresetHintOnAClaimedPort(t *testing.T) {
	r := memReader{
		"compose.yaml":   "services:\n  dev:\n    ports:\n      - '3000:3000'\n  db:\n    ports:\n      - '5432:5432'\n",
		"vite.config.js": "server: { port: 3000 }",
	}
	res := discover(t, r, Options{Presets: testPresets(t, "compose", "bundler")})
	m := routeMap(res)
	if m[3000].Role != config.RoleWeb || m[3000].Label != "dev" || m[5432].Role != config.RoleIgnore {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

func TestDiscoverWebOrder(t *testing.T) {
	preferred := inline(t, "p", "detect: 'true'\ncompose: ports\nports:\n  - {var: MAIN, label: Main, web: preferred}\n")
	fallback := inline(t, "f", "detect: 'true'\ncompose: ports\nports:\n  - {var: DEV, label: Dev, web: fallback}\n  - {var: HOT, label: Hot, web: fallback, role: loopback}\n")
	compose := "services:\n  a:\n    ports:\n      - '8080:80'\n      - '${MAIN:-7000}:7000'\n      - '${DEV:-6000}:6000'\n      - '${HOT:-5000}:5000'\n"

	res := discover(t, memReader{"compose.yaml": compose}, Options{Presets: []*preset.Preset{preferred, fallback}})
	if m := routeMap(res); m[7000].Role != config.RoleWeb || m[8080].Role != config.RoleIgnore {
		t.Errorf("a preferred hint beats container port 80: %+v", res.Routes)
	}

	res = discover(t, memReader{"compose.yaml": compose}, Options{Presets: []*preset.Preset{fallback}})
	if m := routeMap(res); m[8080].Role != config.RoleWeb {
		t.Errorf("container port 80 beats a fallback hint: %+v", res.Routes)
	}

	noEighty := "services:\n  a:\n    ports:\n      - '${DEV:-6000}:6000'\n      - '${HOT:-5000}:5000'\n      - '9000:9000'\n"
	res = discover(t, memReader{"compose.yaml": noEighty}, Options{Presets: []*preset.Preset{fallback}})
	if m := routeMap(res); m[6000].Role != config.RoleWeb || m[5000].Role != config.RoleLoopback {
		t.Errorf("the fallback hint picks the route that is not a loopback: %+v", res.Routes)
	}
}

func TestDiscoverLoneKnownPortIsNotWeb(t *testing.T) {
	p := inline(t, "x", "detect: 'true'\ncompose: ports\nports:\n  - {var: DB_PORT, label: Database}\n  - {var: HOT, label: Hot, role: loopback}\n")
	res := discover(t, memReader{"compose.yaml": "services:\n  db:\n    ports:\n      - '${DB_PORT:-5432}:5432'\n"}, Options{Presets: []*preset.Preset{p}})
	if len(res.Routes) != 1 || res.Routes[0].Role != config.RoleIgnore {
		t.Fatalf("a lone port a preset identified is not the web app: %+v", res.Routes)
	}
	res = discover(t, memReader{".env": "HOT=5174\n"}, Options{Presets: []*preset.Preset{p}})
	if len(res.Routes) != 1 || res.Routes[0].Role != config.RoleLoopback {
		t.Fatalf("a lone loopback route stays a loopback: %+v", res.Routes)
	}
}

func TestDiscoverSuppressAndReplace(t *testing.T) {
	first := inline(t, "first", "detect: 'true'\nports:\n  - {var: A_PORT, label: First}\nroutes:\n  - {id: gone, port: '7000', label: Gone}\n  - {id: kept, port: '7001', label: Kept}\n  - {id: swapped, port: '7002', label: Old}\n")
	second := inline(t, "second", "detect: 'true'\nafter: [first]\nsuppress: [gone]\nports:\n  - {var: A_PORT, label: Second, role: loopback}\nroutes:\n  - {id: swapped, port: '7003', label: New}\n")
	res := discover(t, memReader{".env": "A_PORT=7004\n"}, Options{Presets: []*preset.Preset{second, first}})
	m := routeMap(res)
	if len(res.Routes) != 3 || m[7001].Label != "Kept" || m[7003].Label != "New" || m[7004].Label != "Second (A_PORT)" || m[7004].Role != config.RoleLoopback {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

func TestDiscoverSuggestsOffersThatMatch(t *testing.T) {
	offers := []Offer{
		{Name: "stack", Title: "Test stack", Detect: `compose.contains("LARAVEL_SAIL")`},
		{Name: "other", Title: "Other", Detect: `file("other.config.js")`},
		{Name: "broken", Title: "Broken", Detect: `file(`},
	}
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{Offers: offers})
	if len(res.Suggested) != 1 || res.Suggested[0].Name != "stack" {
		t.Fatalf("Suggested = %+v", res.Suggested)
	}
	if len(res.Applied) != 0 || routeMap(res)[8080].Label != "app (APP_PORT)" {
		t.Errorf("a suggestion must not change the routes: %+v", res.Routes)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("a broken index entry is not the user's problem: %v", res.Warnings)
	}
}

func TestDiscoverWarnsAndCarriesOnWhenAPresetFails(t *testing.T) {
	bad := inline(t, "bad", "detect: env[\"MISSING\"] == \"x\"\n")
	half := inline(t, "half", "detect: 'true'\nroutes:\n  - {id: broken, port: facts.nope, label: Broken}\n  - {id: fine, port: '7000', label: Fine}\n")
	res := discover(t, memReader{}, Options{Presets: []*preset.Preset{bad, half}})
	if len(res.Routes) != 1 || res.Routes[0].Port != 7000 {
		t.Fatalf("routes = %+v", res.Routes)
	}
	if len(res.Warnings) != 2 || !strings.Contains(res.Warnings[0], "preset bad: detect") || !strings.Contains(res.Warnings[1], "preset half: route broken") {
		t.Fatalf("warnings = %q", res.Warnings)
	}
}

func TestDiscoverSkipsPresetsInACycle(t *testing.T) {
	a := inline(t, "a", "detect: 'true'\nafter: [b]\nroutes:\n  - {id: ra, port: '7000', label: A}\n")
	b := inline(t, "b", "detect: 'true'\nafter: [a]\nroutes:\n  - {id: rb, port: '7001', label: B}\n")
	res := discover(t, memReader{".env": "APP_PORT=4000\n"}, Options{Presets: []*preset.Preset{a, b}})
	if len(res.Routes) != 1 || res.Routes[0].Port != 4000 || res.Routes[0].Role != config.RoleWeb {
		t.Fatalf("the core routes should survive: %+v", res.Routes)
	}
	if len(res.Applied) != 0 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "a, b") {
		t.Fatalf("Applied = %v, Warnings = %q", res.Applied, res.Warnings)
	}
}

// failReader fails to read one file that exists.
type failReader struct {
	memReader
	fail string
}

func (f failReader) ReadFile(name string) ([]byte, error) {
	if name == f.fail {
		return nil, errors.New("permission denied")
	}
	return f.memReader.ReadFile(name)
}

func TestDiscoverFailsWhenAPresetFileCannotBeRead(t *testing.T) {
	r := failReader{memReader: memReader{"package.json": `{"devDependencies":{"vite":"1"}}`}, fail: "vite.config.js"}
	_, err := Discover(r, Options{Presets: testPresets(t, "bundler")})
	if err == nil || !strings.Contains(err.Error(), "read vite.config.js: permission denied") {
		t.Fatalf("expected the read error, got %v", err)
	}
}

// An offer's expression failing to read a file never fails discovery.
func TestDiscoverOfferReadErrorsAreNotFatal(t *testing.T) {
	r := failReader{memReader: memReader{"x": "1"}, fail: "other.config.js"}
	offers := []Offer{{Name: "o", Title: "O", Detect: `file("other.config.js")`}}
	res, err := Discover(r, Options{Offers: offers})
	if err != nil {
		t.Fatalf("an offer must not fail discovery: %v", err)
	}
	if len(res.Suggested) != 0 {
		t.Fatalf("Suggested = %+v", res.Suggested)
	}
}

func TestDiscoverDetectOnADirectoryIsAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := inline(t, "d", "detect: 'file(\"vendor\")'\n")
	res := discover(t, DirReader{Root: dir}, Options{Presets: []*preset.Preset{p}})
	if len(res.Applied) != 0 {
		t.Fatalf("Applied = %v", res.Applied)
	}
}

func TestDiscoverDetectUnderAFileIsAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := inline(t, "d", "detect: 'file(\"bin/console\")'\n")
	res := discover(t, DirReader{Root: dir}, Options{Presets: []*preset.Preset{p}})
	if len(res.Applied) != 0 {
		t.Fatalf("Applied = %v", res.Applied)
	}
}

// countReader counts reads per file.
type countReader struct {
	memReader
	reads map[string]int
}

func (c countReader) ReadFile(name string) ([]byte, error) {
	c.reads[name]++
	return c.memReader.ReadFile(name)
}

func TestDiscoverReadsEachFileOnce(t *testing.T) {
	r := countReader{memReader: memReader{"vite.config.js": "server: { port: 3000 }", "package.json": "{}"}, reads: map[string]int{}}
	offers := []Offer{{Name: "o", Title: "O", Detect: `file("vite.config.js") && pkg("vite")`}}
	discover(t, r, Options{Presets: testPresets(t, "bundler"), Offers: offers})
	for name, n := range r.reads {
		if n != 1 {
			t.Errorf("%s was read %d times", name, n)
		}
	}
	if r.reads["vite.config.js"] != 1 {
		t.Errorf("vite.config.js should have been read: %v", r.reads)
	}
}

func TestMemoKeepsMissingFilesMissing(t *testing.T) {
	m := newMemo(memReader{})
	for i := 0; i < 2; i++ {
		if _, err := m.ReadFile("nope"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("expected fs.ErrNotExist, got %v", err)
		}
	}
	if m.err != nil {
		t.Errorf("a missing file is not a failure: %v", m.err)
	}
}

func TestConfigRoutes(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{})
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

// sourced parses a preset written in the test as one installed from the
// registry with the given alias.
func sourced(t *testing.T, source, name, body string) *preset.Preset {
	t.Helper()
	p := inline(t, name, body)
	p.Source = source
	return p
}

// A plain name in a registry preset means the preset of that name from the
// same registry; a local preset reaches a registry preset by its qualified
// name.
func TestDiscoverResolvesReferencesWithinTheirSource(t *testing.T) {
	base := "detect: 'true'\nfacts:\n  port: {scan: [%s], regex: '(\\d+)', as: int}\n"
	aBase := sourced(t, "a", "base", fmt.Sprintf(base, "a.txt"))
	bBase := sourced(t, "b", "base", fmt.Sprintf(base, "b.txt"))
	aUser := sourced(t, "a", "user", "detect: 'true'\nafter: [base]\nroutes:\n"+
		"  - {id: a-user, when: 'active(\"base\")', port: 'presets[\"base\"].port + 1', label: A user}\n")
	lonely := sourced(t, "c", "lonely", "detect: 'true'\nroutes:\n"+
		"  - {id: lonely, when: '!active(\"base\") && !(\"base\" in presets) && !active(\"a/base\")', port: '7100', label: Lonely}\n")
	mine := inline(t, "mine", "detect: 'true'\nafter: [b/base]\nroutes:\n"+
		"  - {id: mine, when: 'active(\"b/base\")', port: 'presets[\"b/base\"].port + 2', label: Mine}\n"+
		"  - {id: mine-plain, when: 'active(\"base\")', port: '7200', label: Plain}\n")
	res := discover(t, memReader{"a.txt": "7000", "b.txt": "9000"}, Options{Presets: []*preset.Preset{mine, lonely, aUser, bBase, aBase}})
	if got := strings.Join(res.Applied, ","); got != "a/base,a/user,b/base,c/lonely,mine" {
		t.Errorf("Applied = %s", got)
	}
	m := routeMap(res)
	if m[7001].Label != "A user" || m[7100].Label != "Lonely" || m[9002].Label != "Mine" || m[9002].Preset != "mine" || m[7001].Preset != "a/user" {
		t.Errorf("routes = %+v", res.Routes)
	}
	if _, ok := m[9001]; ok {
		t.Error("a/user saw b/base")
	}
	if _, ok := m[7200]; ok {
		t.Error("a plain name in a local preset reached a registry preset")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %q", res.Warnings)
	}
}

// Route ids share one namespace across registries: a later preset replaces
// an id, whichever registry it came from.
func TestDiscoverIdsAreSharedAcrossRegistries(t *testing.T) {
	first := sourced(t, "a", "web", "detect: 'true'\nroutes:\n  - {id: app, port: '7000', label: First}\n")
	second := sourced(t, "b", "web", "detect: 'true'\nroutes:\n  - {id: app, port: '7001', label: Second}\n")
	res := discover(t, memReader{}, Options{Presets: []*preset.Preset{second, first}})
	if len(res.Routes) != 1 || res.Routes[0].Port != 7001 || res.Routes[0].Preset != "b/web" {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

func TestDiscoverWarningsNameRegistryPresetsInFull(t *testing.T) {
	bad := sourced(t, "acme", "bad", "detect: env[\"MISSING\"] == \"x\"\n")
	res := discover(t, memReader{}, Options{Presets: []*preset.Preset{bad}})
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "preset acme/bad: detect") {
		t.Fatalf("warnings = %q", res.Warnings)
	}
}

func TestResultComposeUnread(t *testing.T) {
	compose := DirReader{Root: "testdata/compose-literal"}
	if !discover(t, compose, Options{}).ComposeUnread() {
		t.Error("a Compose file no preset asked for is unread")
	}
	if discover(t, compose, Options{Presets: testPresets(t, "compose")}).ComposeUnread() {
		t.Error("a Compose file a preset asked for is read")
	}
	if discover(t, DirReader{Root: "testdata/empty"}, Options{}).ComposeUnread() {
		t.Error("there is no Compose file to leave unread")
	}
}

// inline2 parses a schema 2 preset written in the test.
func inline2(t *testing.T, name, body string) *preset.Preset {
	t.Helper()
	p, err := preset.Parse(name, []byte("schema: 2\nname: "+name+"\ntitle: T\n"+body))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// routeLines shows ConfigRoutes as "port role [hosts]".
func routeLines(res *Result) string {
	var out []string
	for _, r := range res.ConfigRoutes() {
		out = append(out, fmt.Sprintf("%d %s %v", r.Port, r.Role, r.Hosts))
	}
	return strings.Join(out, "\n")
}

const hostsCompose = `services:
  app:
    ports:
      - '${APP_PORT:-80}:80'
      - '${FORWARD_REVERB_PORT:-8080}:8080'
`

func hostsPreset(t *testing.T, appHosts string) *preset.Preset {
	return inline2(t, "h", `detect: 'true'
compose: ports
routes:
  - id: app
    port: env_port("APP_PORT", 80)
    label: App
    hosts: '`+appHosts+`'
  - id: reverb
    port: env_port("FORWARD_REVERB_PORT", 8080)
    label: Reverb
    hosts: '[url_host(env["REVERB_HOST"])]'
`)
}

// Hosts a preset infers make their routes web, with the primary first even
// though the other has the lower port.
func TestDiscoverInfersRouteHosts(t *testing.T) {
	r := memReader{
		"compose.yaml": hostsCompose,
		".env":         "APP_URL=https://app.local.x.io:8220\nAPP_PORT=8220\nFORWARD_REVERB_PORT=6001\nREVERB_HOST=ws.local.x.io\n",
	}
	res := discover(t, r, Options{Presets: []*preset.Preset{hostsPreset(t, `[url_host(env["APP_URL"])]`)}})
	if want := "8220 web [app.local.x.io]\n6001 web [ws.local.x.io]"; routeLines(res) != want {
		t.Fatalf("routes:\n%s\nwant:\n%s", routeLines(res), want)
	}
	if l := res.Routes[0].Label; l != "app (APP_PORT)" {
		t.Errorf("the Compose label should be kept, got %q", l)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestDiscoverFiltersInferredHosts(t *testing.T) {
	r := memReader{
		"compose.yaml": hostsCompose,
		".env":         "REVERB_HOST=app.x.io\n",
	}
	list := `["localhost", "127.0.0.1", "::1", "myapp", "", "App.X.io.", "bad_host.x.io", "app.x.io"]`
	res := discover(t, r, Options{Presets: []*preset.Preset{hostsPreset(t, list)}})
	m := routeMap(res)
	if got := m[80].Hosts; strings.Join(got, ",") != "app.x.io" {
		t.Errorf("hosts = %v", got)
	}
	if len(m[8080].Hosts) != 0 || m[8080].Role == config.RoleWeb {
		t.Errorf("a host an earlier route holds should be left off: %+v", m[8080])
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "bad_host.x.io") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestDiscoverStockSailInfersNoHosts(t *testing.T) {
	r := memReader{
		"compose.yaml": hostsCompose,
		".env":         "APP_URL=http://localhost\nREVERB_HOST=localhost\n",
	}
	with := discover(t, r, Options{Presets: []*preset.Preset{hostsPreset(t, `[url_host(env["APP_URL"])]`)}})
	without := discover(t, r, Options{Presets: []*preset.Preset{inline2(t, "h", `detect: 'true'
compose: ports
routes:
  - {id: app, port: 'env_port("APP_PORT", 80)', label: App}
  - {id: reverb, port: 'env_port("FORWARD_REVERB_PORT", 8080)', label: Reverb}
`)}})
	if routeLines(with) != routeLines(without) {
		t.Fatalf("routes:\n%s\nwithout the hosts:\n%s", routeLines(with), routeLines(without))
	}
	web := 0
	for _, c := range with.Routes {
		if len(c.Hosts) != 0 {
			t.Errorf("hosts = %v", c.Hosts)
		}
		if c.Role == config.RoleWeb {
			web++
		}
	}
	if web != 1 {
		t.Errorf("%d web routes, want 1", web)
	}
}

func TestDiscoverHostsOnLoopbackPortDropped(t *testing.T) {
	r := memReader{"compose.yaml": hostsCompose, ".env": "REVERB_HOST=ws.x.io\n"}
	p := inline2(t, "h", `detect: 'true'
compose: ports
ports:
  - {var: FORWARD_REVERB_PORT, label: Reverb, role: loopback}
routes:
  - {id: reverb, port: 'env_port("FORWARD_REVERB_PORT", 8080)', label: Reverb, hosts: '[url_host(env["REVERB_HOST"])]'}
`)
	res := discover(t, r, Options{Presets: []*preset.Preset{p}})
	c := routeMap(res)[8080]
	if c.Role != config.RoleLoopback || len(c.Hosts) != 0 {
		t.Errorf("candidate = %+v", c)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "port 8080") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}
