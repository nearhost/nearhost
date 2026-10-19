package nearhostrc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func TestMergePutsFileRoutesFirstAndKeepsManualHosts(t *testing.T) {
	old := config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{
		{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "mine.local.dd.io"}}, // mine: added by hand
		{Port: 5432, Role: config.RoleLoopback},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"files.local.dd.io"}},
	}, Nearhostrc: &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}}
	f := &File{SHA256: "new", Routes: []Route{{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}
	got := mustMerge(t, old, f, nil)
	want := []config.Route{
		// The port moved: "mine" was added by hand to 8220, which the file
		// no longer lists, so it follows app.local.dd.io to 8221.
		{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "mine.local.dd.io"}},
		{Port: 8220, Role: config.RoleIgnore, Hosts: nil},
		{Port: 5432, Role: config.RoleLoopback},
		{Port: 9000, Role: config.RoleIgnore},
	}
	if !reflect.DeepEqual(got.Routes, want) || got.Nearhostrc.SHA256 != "new" {
		t.Fatalf("Merge = %+v", got)
	}
}

func TestMergeMovesManualHostsOfADroppedPortToThePrimary(t *testing.T) {
	old := config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{
		{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"files.local.dd.io", "mine.local.dd.io", "app.local.dd.io"}}, // mine: added by hand
	}, Nearhostrc: &config.RcSnapshot{SHA256: "old", Routes: []config.Route{
		{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"files.local.dd.io"}},
	}}}
	// No file route holds files.local.dd.io any more, so the hand-added host
	// on 9000 goes to the primary web route. app.local.dd.io is in the file,
	// so it is served once, where the file puts it.
	f := &File{SHA256: "new", Routes: []Route{
		{Port: 5432, Role: config.RoleLoopback},
		{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}},
		{Port: 8230, Role: config.RoleWeb, Hosts: []string{"ws.local.dd.io"}},
	}}
	got := mustMerge(t, old, f, nil)
	want := []config.Route{
		{Port: 5432, Role: config.RoleLoopback},
		{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "mine.local.dd.io"}},
		{Port: 8230, Role: config.RoleWeb, Hosts: []string{"ws.local.dd.io"}},
		{Port: 8220, Role: config.RoleIgnore},
		{Port: 9000, Role: config.RoleIgnore},
	}
	if !reflect.DeepEqual(got.Routes, want) {
		t.Fatalf("Merge = %+v", got.Routes)
	}
}

func TestMergeKeepsManualHostsOnAPortTheFileStillLists(t *testing.T) {
	old := config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{
		{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "mine.local.dd.io"}},
	}, Nearhostrc: &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}}
	f := &File{SHA256: "new", Routes: []Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "api.local.dd.io"}}}}
	got := mustMerge(t, old, f, nil)
	want := []string{"app.local.dd.io", "api.local.dd.io", "mine.local.dd.io"}
	if len(got.Routes) != 1 || !reflect.DeepEqual(got.Routes[0].Hosts, want) {
		t.Fatalf("Merge = %+v", got.Routes)
	}
}

func TestNeedsApproval(t *testing.T) {
	old := config.Project{Alias: "dd.test", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}
	next := config.Project{Alias: "dd.test", Routes: []config.Route{
		{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "x.test", "*.status.local.dd.io", "*.dd.test"}},
		{Port: 8230, Role: config.RoleWeb, Hosts: []string{"localhost"}},
	}}
	want := []Item{{8221, "*.dd.test", "a wildcard"}, {8221, "*.status.local.dd.io", "a wildcard"}, {8230, "localhost", "localhost"}}
	if got := NeedsApproval(old, next); !reflect.DeepEqual(got, want) {
		t.Fatalf("NeedsApproval = %+v", got)
	}
	if got := NeedsApproval(next, old); len(got) != 0 {
		t.Fatalf("removing hosts needs no approval, got %+v", got)
	}
}

// mustMerge merges and checks that config.Validate accepts the result.
func mustMerge(t *testing.T, p config.Project, f *File, discovered []config.Route) config.Project {
	t.Helper()
	got, err := Merge(p, f, discovered)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	p2 := got
	p2.Server = config.LocalServer
	cfg := &config.Config{Projects: []config.Project{p2}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("merged project is invalid: %v\n%+v", err, got)
	}
	return got
}

func TestMergeResultsAreValid(t *testing.T) {
	web := func(port int, hosts ...string) Route { return Route{Port: port, Role: config.RoleWeb, Hosts: hosts} }
	cases := map[string]struct {
		old config.Project
		f   *File
	}{
		"manual host also in file on another port": {
			config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"mine.dd.io"}}}},
			&File{Routes: []Route{web(8220, "a.dd.io"), web(9, "mine.dd.io")}},
		},
		"same on first adoption, nil snapshot": {
			config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"mine.dd.io"}}}},
			&File{Routes: []Route{web(8220), web(9, "mine.dd.io")}},
		},
		"all other web routes demoted": {
			config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}, {Port: 81, Role: config.RoleWeb, Hosts: []string{"b.dd.io"}}}},
			&File{Routes: []Route{{Port: 5432, Role: config.RoleLoopback}, web(82)}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { mustMerge(t, c.old, c.f, nil) })
	}
}

func TestMergeRejectsAliasOnNonPrimaryRoute(t *testing.T) {
	p := config.Project{Name: "dd", Alias: "dd.test"}
	f := &File{Routes: []Route{{Port: 1, Role: config.RoleWeb, Hosts: []string{"a.dd.io"}}, {Port: 2, Role: config.RoleWeb, Hosts: []string{"DD.test"}}}}
	_, err := Merge(p, f, nil)
	if err == nil || !strings.HasPrefix(err.Error(), ".nearhostrc: port 2 lists") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsRoutesThatCannotBeServed(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"duplicate port":                 {"version: 1\nroutes:\n  - port: 1\n  - port: 1\n    hosts: [a.dd.io]\n", "port 1 is listed twice"},
		"same host":                      {"version: 1\nroutes:\n  - port: 1\n    hosts: [a.dd.io]\n  - port: 2\n    hosts: [a.dd.io]\n", `host "a.dd.io" (port 2)`},
		"no web route":                   {"version: 1\nroutes:\n  - port: 1\n    role: loopback\n", "needs at least one web route"},
		"second web route without hosts": {"version: 1\nroutes:\n  - port: 1\n  - port: 2\n", "port 2 needs at least one host"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml), nil)
			if err == nil || !strings.HasPrefix(err.Error(), ".nearhostrc") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
