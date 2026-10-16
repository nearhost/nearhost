package nearhostrc

import (
	"reflect"
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
	got := Merge(old, f, nil)
	want := []config.Route{
		{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}, // port moved; "mine" was on 8220, which the file no longer lists
		{Port: 8220, Role: config.RoleIgnore, Hosts: nil},
		{Port: 5432, Role: config.RoleLoopback},
		{Port: 9000, Role: config.RoleIgnore},
	}
	if !reflect.DeepEqual(got.Routes, want) || got.Nearhostrc.SHA256 != "new" {
		t.Fatalf("Merge = %+v", got)
	}
}

func TestMergeKeepsManualHostsOnAPortTheFileStillLists(t *testing.T) {
	old := config.Project{Name: "dd", Alias: "dd.test", Routes: []config.Route{
		{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "mine.local.dd.io"}},
	}, Nearhostrc: &config.RcSnapshot{SHA256: "old", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}}
	f := &File{SHA256: "new", Routes: []Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "api.local.dd.io"}}}}
	got := Merge(old, f, nil)
	want := []string{"app.local.dd.io", "api.local.dd.io", "mine.local.dd.io"}
	if len(got.Routes) != 1 || !reflect.DeepEqual(got.Routes[0].Hosts, want) {
		t.Fatalf("Merge = %+v", got.Routes)
	}
}

func TestNeedsApproval(t *testing.T) {
	old := config.Project{Alias: "dd.test", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io"}}}}
	next := config.Project{Alias: "dd.test", Routes: []config.Route{
		{Port: 8221, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "x.test", "*.status.local.dd.io"}},
		{Port: 8230, Role: config.RoleWeb, Hosts: []string{"localhost"}},
	}}
	want := []Item{{8221, "*.status.local.dd.io", "a wildcard"}, {8230, "localhost", "localhost"}}
	if got := NeedsApproval(old, next); !reflect.DeepEqual(got, want) {
		t.Fatalf("NeedsApproval = %+v", got)
	}
	if got := NeedsApproval(next, old); len(got) != 0 {
		t.Fatalf("removing hosts needs no approval, got %+v", got)
	}
}
