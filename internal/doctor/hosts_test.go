package doctor

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hosts"
)

// dns is a fake lookup. A name it does not hold gets no answer; a name in
// errs fails. It records what it was asked.
type dns struct {
	mu    sync.Mutex
	addrs map[string][]string
	errs  map[string]bool
	asked []string
	never string // a suffix the test fails on
	t     *testing.T
}

func (d *dns) lookup() hosts.Lookup {
	return func(_ context.Context, name string) ([]netip.Addr, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.asked = append(d.asked, name)
		if d.never != "" && strings.HasSuffix(name, d.never) {
			d.t.Errorf("looked up %s", name)
		}
		if d.errs[name] {
			return nil, errors.New("servfail")
		}
		var out []netip.Addr
		for _, a := range d.addrs[name] {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

func webProject(name, alias string, enabled bool, hs ...string) config.Project {
	return config.Project{Name: name, Server: config.LocalServer, Alias: alias, Enabled: enabled,
		Routes: []config.Route{{Port: 8000, Role: config.RoleWeb, Hosts: hs}}}
}

func hostIn(p config.Project, d *dns, mutate func(*Input)) []Finding {
	in := Input{Project: p, Added: true, Files: files{}, Lookup: d.lookup(), ManagesHosts: true}
	if mutate != nil {
		mutate(&in)
	}
	return Check(context.Background(), in)
}

func has(fs []Finding, id string) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

func TestUnresolvedHosts(t *testing.T) {
	loop := map[string][]string{"auth.local.x.io": {"127.0.0.1"}}
	p := webProject("app", "app.test", true, "auth.local.x.io", "*.local.x.pro")

	got := byID(t, hostIn(p, &dns{addrs: loop}, nil), "hosts.unresolved")
	if got.Severity != SeverityError || got.Fix.Kind != FixNearhost || !strings.Contains(got.Message, "app.test") ||
		got.Fix.Text != "Start the project in Nearhost; it adds app.test to /etc/hosts." {
		t.Errorf("finding = %+v", got)
	}
	for _, f := range hostIn(p, &dns{addrs: loop}, nil) {
		if f.ID == "hosts.unresolved" && strings.Contains(f.Message, "auth.local.x.io") {
			t.Errorf("resolving name reported: %+v", f)
		}
	}

	cases := map[string]func(*Input){
		"in the block": func(in *Input) { in.Block = []string{"APP.test"} },
		"not managed":  func(in *Input) { in.ManagesHosts = false },
		"disabled":     func(in *Input) { in.Project.Enabled = false },
		"not added":    func(in *Input) { in.Added = false },
		"no lookup":    func(in *Input) { in.Lookup = nil },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if has(hostIn(p, &dns{addrs: loop}, m), "hosts.unresolved") {
				t.Error("reported")
			}
		})
	}

	// A name that resolves elsewhere is as unreachable as one that doesn't.
	q := webProject("api", "", true, "api.x.io")
	d := &dns{addrs: map[string][]string{"api.x.io": {"93.184.216.34"}}}
	if !has(hostIn(q, d, nil), "hosts.unresolved") {
		t.Error("public address not reported")
	}
}

func TestPublicHosts(t *testing.T) {
	d := &dns{addrs: map[string][]string{"app.example.com": {"93.184.216.34"}}, never: ".test", t: t}
	p := webProject("app", "app.test", true, "app.example.com")
	got := byID(t, hostIn(p, d, func(in *Input) { in.Block = []string{"app.test", "app.example.com"} }), "hosts.public")
	if got.Severity != SeverityWarning || got.Fix.Kind != FixManual || !strings.Contains(got.Message, "93.184.216.34") ||
		!strings.Contains(got.Message, "app.example.com") {
		t.Errorf("finding = %+v", got)
	}
	if got.Fix.Text != "Nothing to do if that is what you want; otherwise use a name that resolves to 127.0.0.1, or a .test name." {
		t.Errorf("fix = %q", got.Fix.Text)
	}
	// Not added still warns.
	if !has(hostIn(p, d, func(in *Input) { in.Added = false }), "hosts.public") {
		t.Error("not reported for a project not added")
	}
}

func TestUnresolvedPattern(t *testing.T) {
	p := webProject("app", "app.test", true, "*.local.x.pro", "internal.*.local.x.pro")
	d := &dns{addrs: map[string][]string{"internal.nearhost-check.local.x.pro": {"127.0.0.1"}}}
	got := hostIn(p, d, func(in *Input) { in.Block = []string{"app.test"} })
	f := byID(t, got, "hosts.unresolved-pattern")
	if f.Severity != SeverityError || f.Fix.Kind != FixManual || !strings.Contains(f.Message, "*.local.x.pro") ||
		!strings.Contains(f.Fix.Text, "Add a DNS record for *.local.x.pro pointing at 127.0.0.1") ||
		!strings.Contains(f.Fix.Text, "hosts file can't hold wildcards") {
		t.Errorf("finding = %+v", f)
	}
	n := 0
	for _, g := range got {
		if g.ID == "hosts.unresolved-pattern" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d findings, want 1 (the internal.* pattern resolves)", n)
	}
	asked := strings.Join(d.asked, " ")
	for _, w := range []string{"nearhost-check.local.x.pro", "internal.nearhost-check.local.x.pro"} {
		if !strings.Contains(asked, w) {
			t.Errorf("never asked for %s: %v", w, d.asked)
		}
	}

	e := &dns{errs: map[string]bool{"nearhost-check.local.x.pro": true}}
	if has(hostIn(webProject("a", "a.test", true, "*.local.x.pro"), e, nil), "hosts.unresolved-pattern") {
		t.Error("a lookup error was reported")
	}
	ok := &dns{addrs: map[string][]string{"nearhost-check.local.x.pro": {"127.0.0.1"}}}
	if has(hostIn(webProject("a", "a.test", true, "*.local.x.pro"), ok, nil), "hosts.unresolved-pattern") {
		t.Error("a loopback answer was reported")
	}
}

func TestOverlappingPatterns(t *testing.T) {
	mine := webProject("app", "app.test", true, "*.local.x.pro")
	other := webProject("other", "other.test", true, "internal.*.x.pro")
	d := func() *dns { return &dns{} }
	got := hostIn(mine, d(), func(in *Input) { in.Others = []config.Project{other} })
	var found []Finding
	for _, f := range got {
		if f.ID == "hosts.overlap" {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		t.Fatalf("findings = %+v", found)
	}
	f := found[0]
	if f.Severity != SeverityWarning || f.Fix.Kind != FixProject ||
		!strings.Contains(f.Message, "internal.*.x.pro") || !strings.Contains(f.Message, "other") ||
		f.Fix.Text != "If that is not what you want, make the patterns distinct." {
		t.Errorf("finding = %+v", f)
	}

	off := other
	off.Enabled = false
	if has(hostIn(mine, d(), func(in *Input) { in.Others = []config.Project{off} }), "hosts.overlap") {
		t.Error("disabled project reported")
	}
	exact := webProject("other", "other.test", true, "a.local.x.pro")
	if has(hostIn(mine, d(), func(in *Input) { in.Others = []config.Project{exact} }), "hosts.overlap") {
		t.Error("exact host reported")
	}
	same := webProject("other", "other.test", true, "*.local.x.pro")
	if has(hostIn(mine, d(), func(in *Input) { in.Others = []config.Project{same} }), "hosts.overlap") {
		t.Error("equal patterns reported")
	}
}

func TestNotListening(t *testing.T) {
	p := webProject("app", "app.test", true, "app.example.test")
	down := func(context.Context, int) bool { return false }
	run := func(m func(*Input)) []Finding {
		return hostIn(p, &dns{}, func(in *Input) {
			in.Block = []string{"app.test", "app.example.test"}
			in.Listening = down
			if m != nil {
				m(in)
			}
		})
	}
	f := byID(t, run(func(in *Input) { in.Running = true }), "routes.not-listening")
	if f.Severity != SeverityWarning || f.Fix.Kind != FixManual || !strings.Contains(f.Message, "app.example.test") ||
		f.Fix.Text != "Start the project's dev server on port 8000, or correct the port." {
		t.Errorf("finding = %+v", f)
	}
	if got := byID(t, run(nil), "routes.not-listening"); got.Severity != SeverityInfo {
		t.Errorf("severity = %s", got.Severity)
	}
	if has(run(func(in *Input) { in.Project.Server = "box" }), "routes.not-listening") {
		t.Error("remote project reported")
	}
	if has(run(func(in *Input) { in.Listening = nil }), "routes.not-listening") {
		t.Error("nil Listening reported")
	}
	if has(run(func(in *Input) { in.Listening = func(context.Context, int) bool { return true } }), "routes.not-listening") {
		t.Error("listening port reported")
	}

	// A web route without hosts of its own is named by the main name.
	second := run(func(in *Input) {
		in.Project.Routes = append(in.Project.Routes, config.Route{Port: 5173, Role: config.RoleWeb})
		in.Listening = func(_ context.Context, port int) bool { return port != 5173 }
	})
	if f := byID(t, second, "routes.not-listening"); f.Message != "nothing listens on port 5173, which serves app.test" {
		t.Errorf("a route without hosts: %q", f.Message)
	}
}
