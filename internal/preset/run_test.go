package preset

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func named(t *testing.T, name string, extra string) *Preset {
	t.Helper()
	return mustParse(t, name, "schema: 1\nname: "+name+"\ntitle: T\ndetect: 'true'\n"+extra)
}

func names(ps []*Preset) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}

func TestOrder(t *testing.T) {
	a := named(t, "a", "after: [z]\n")
	b := named(t, "b", "")
	z := named(t, "z", "after: [missing]\n")
	got, err := Order([]*Preset{z, b, a})
	if err != nil || names(got) != "b,z,a" {
		t.Fatalf("Order = %s %v, want b,z,a", names(got), err)
	}
	if got, err := Order(nil); err != nil || len(got) != 0 {
		t.Fatalf("Order(nil) = %v %v", got, err)
	}
}

func TestOrderLeavesOutACycle(t *testing.T) {
	a := named(t, "a", "after: [b]\n")
	b := named(t, "b", "after: [a]\n")
	c := named(t, "c", "")
	got, err := Order([]*Preset{a, b, c})
	if names(got) != "c" {
		t.Errorf("Order = %s, want only c", names(got))
	}
	if err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Errorf("error %v should name a and b", err)
	}
}

func TestOwners(t *testing.T) {
	first := named(t, "first", "ports:\n  - {var: A_PORT, label: A}\nroutes:\n  - {id: r1, port: '1', label: R}\n  - {id: r2, port: '2', label: R}\nadvisories:\n  - {id: a1, when: 'true', message: M}\n  - {id: a2, when: 'true', message: M}\n")
	second := named(t, "second", "suppress: [r1, a1, nothing]\nports:\n  - {var: A_PORT, label: Other}\nadvisories:\n  - {id: a2, when: 'true', message: Replaced}\n")
	got := Owners([]*Preset{first, second})
	want := map[string]string{"A_PORT": "second", "r2": "first", "a2": "second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Owners = %v, want %v", got, want)
	}
}

func all(string) bool { return true }

func TestRoutes(t *testing.T) {
	p := named(t, "x", `facts:
  port: {scan: [x.js], regex: 'port:(\d+)', as: int}
routes:
  - {id: found, when: facts.port > 0, port: facts.port, label: Found, web: fallback}
  - {id: off, when: 'false', port: '9', label: Off}
  - {id: too-big, port: '70000', label: Big}
  - {id: zero, port: '0', label: Zero}
  - {id: broken, port: facts.missing, label: Broken}
  - {id: other, when: 'active("y") && presets["y"].port == 4000', port: 'presets["y"].port', label: Other, role: loopback}
  - {id: dropped, port: '7', label: Dropped}
`)
	pr := &Project{Files: newFiles(map[string]string{"x.js": "port:3000"})}
	c := Context{
		Project: pr,
		Facts:   p.EvalFacts(pr),
		Earlier: map[string]map[string]any{"y": {"port": int64(4000)}},
		Active:  func(name string) bool { return name == "y" },
	}
	got, errs := p.EvalRoutes(c, func(id string) bool { return id != "dropped" })
	want := []RouteOut{
		{ID: "found", Port: 3000, Label: "Found", Role: config.RoleIgnore, Web: WebFallback},
		{ID: "other", Port: 4000, Label: "Other", Role: config.RoleLoopback},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Routes = %+v, want %+v", got, want)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "preset x: route broken") {
		t.Errorf("errs = %v, want one about the broken route", errs)
	}
}

func TestRoutesWithAnEmptyContext(t *testing.T) {
	p := named(t, "x", "routes:\n  - {id: a, port: '8000', label: A}\n")
	got, errs := p.EvalRoutes(Context{Project: &Project{}}, all)
	if len(errs) != 0 || len(got) != 1 || got[0].Port != 8000 {
		t.Fatalf("Routes = %+v %v", got, errs)
	}
}

func TestAdvice(t *testing.T) {
	p := named(t, "x", `advisories:
  - id: shown
    when: '"APP_URL" in env'
    message: "  Set APP_URL to https://{{ alias }}.  "
    snippet: |
      APP_URL=https://{{ alias }}
      PORT={{ free_port(5173) }}
  - {id: hidden, when: 'false', message: Hidden}
  - {id: broken, when: facts.missing, message: Broken}
  - {id: bad-message, when: 'true', message: 'Port {{ facts.missing }}'}
  - {id: dropped, when: 'true', message: Dropped}
  - {id: bare, when: 'true', message: Bare}
`)
	c := Context{Project: &Project{Env: map[string]string{"APP_URL": "x"}}}
	got, errs := p.EvalAdvice(c, "myapp.test", func(from int) int { return from + 2 }, func(id string) bool { return id != "dropped" })
	want := []AdviceOut{
		{ID: "shown", Message: "Set APP_URL to https://myapp.test.", Snippet: "APP_URL=https://myapp.test\nPORT=5175"},
		{ID: "bare", Message: "Bare"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Advice = %+v, want %+v", got, want)
	}
	if len(errs) != 2 || !strings.Contains(errs[0].Error(), "preset x: advisory broken") || !strings.Contains(errs[1].Error(), "advisory bad-message") {
		t.Errorf("errs = %v, want the broken and bad-message advisories", errs)
	}
}

// from is a test preset that came from the registry with the given alias.
func from(t *testing.T, source, name, extra string) *Preset {
	t.Helper()
	p := named(t, name, extra)
	p.Source = source
	return p
}

func keys(ps []*Preset) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Key())
	}
	return strings.Join(out, ",")
}

func TestKeyAndResolve(t *testing.T) {
	local := named(t, "mine", "")
	remote := from(t, "acme", "stack", "")
	if local.Key() != "mine" || remote.Key() != "acme/stack" {
		t.Errorf("keys = %q, %q", local.Key(), remote.Key())
	}
	for _, tc := range []struct {
		p          *Preset
		name, want string
	}{
		{local, "other", "other"},
		{local, "acme/other", "acme/other"},
		{remote, "other", "acme/other"},
		{remote, "team/other", ""},
	} {
		if got := tc.p.Resolve(tc.name); got != tc.want {
			t.Errorf("%s.Resolve(%q) = %q, want %q", tc.p.Key(), tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		p, other *Preset
		want     string
		ok       bool
	}{
		{local, named(t, "other", ""), "other", true},
		{local, from(t, "team", "other", ""), "team/other", true},
		{remote, from(t, "acme", "other", ""), "other", true},
		{remote, from(t, "team", "other", ""), "", false},
		{remote, named(t, "other", ""), "", false},
	} {
		if got, ok := tc.p.NameOf(tc.other); got != tc.want || ok != tc.ok {
			t.Errorf("%s.NameOf(%s) = %q %v, want %q %v", tc.p.Key(), tc.other.Key(), got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseAcceptsAQualifiedAfter(t *testing.T) {
	p := named(t, "mine", "after: [acme/stack, bundler]\n")
	if len(p.After) != 2 {
		t.Fatalf("After = %v", p.After)
	}
}

// In a registry preset a plain name means the same registry, and a qualified
// name means nothing. In a local preset a qualified name means that
// registry's preset. Ties are broken by the qualified name.
func TestOrderResolvesNamesWithinTheirSource(t *testing.T) {
	first := from(t, "a", "first", "after: [second]\n")
	second := from(t, "a", "second", "after: [b/third]\n")
	third := from(t, "b", "third", "after: [first]\n")
	other := from(t, "b", "second", "")
	mine := named(t, "aaa", "after: [b/third, second]\n")
	got, err := Order([]*Preset{mine, third, other, second, first})
	if err != nil || keys(got) != "a/second,a/first,b/second,b/third,aaa" {
		t.Fatalf("Order = %s %v", keys(got), err)
	}
}

func TestOrderNamesACycleByQualifiedName(t *testing.T) {
	a := from(t, "acme", "a", "after: [b]\n")
	b := from(t, "acme", "b", "after: [a]\n")
	_, err := Order([]*Preset{a, b})
	if err == nil || !strings.Contains(err.Error(), "acme/a, acme/b") {
		t.Errorf("error %v should name acme/a and acme/b", err)
	}
}

func TestOwnersAreQualified(t *testing.T) {
	first := named(t, "first", "routes:\n  - {id: r1, port: '1', label: R}\n")
	second := from(t, "acme", "first", "routes:\n  - {id: r2, port: '2', label: R}\n")
	got := Owners([]*Preset{first, second})
	want := map[string]string{"r1": "first", "r2": "acme/first"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Owners = %v, want %v", got, want)
	}
}

func TestErrorsNameThePresetByItsQualifiedName(t *testing.T) {
	p := from(t, "acme", "x", "routes:\n  - {id: broken, port: facts.missing, label: Broken}\n")
	_, errs := p.EvalRoutes(Context{Project: &Project{}}, all)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "preset acme/x: route broken") {
		t.Errorf("errs = %v", errs)
	}
}
