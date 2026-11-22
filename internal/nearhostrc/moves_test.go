package nearhostrc

import (
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// Moves lists a host moved or gone, and a route gone with its hosts.
func TestMovesSaysWhatGoes(t *testing.T) {
	old := config.Project{Routes: []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"a.local.dd.io", "b.local.dd.io"}},
		{Port: 5173, Role: config.RoleLoopback, Label: "vite"},
		{Port: 3306, Role: config.RoleIgnore},
	}}
	next := config.Project{Routes: []config.Route{{Port: 8080, Role: config.RoleWeb, Hosts: []string{"a.local.dd.io"}}}}
	got := strings.Join(Moves(old, next), "|")
	if want := "a.local.dd.io port 80 → 8080|b.local.dd.io port 80 → removed|port 5173 (vite) → removed"; got != want {
		t.Errorf("Moves = %q, want %q", got, want)
	}
}

// The project's alias is served on its first web route, so it moves or goes
// as the route's own hosts do.
func TestMovesSaysWhereTheAliasGoes(t *testing.T) {
	old := config.Project{Alias: "shop.test", Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}
	moved := config.Project{Alias: "shop.test", Routes: []config.Route{{Port: 4000, Role: config.RoleWeb}}}
	if got, want := strings.Join(Moves(old, moved), "|"), "shop.test port 3000 → 4000"; got != want {
		t.Errorf("port moved: Moves = %q, want %q", got, want)
	}
	renamed := config.Project{Alias: "store.test", Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}
	if got, want := strings.Join(Moves(old, renamed), "|"), "shop.test port 3000 → removed"; got != want {
		t.Errorf("alias renamed: Moves = %q, want %q", got, want)
	}
	if got := Moves(old, old); len(got) != 0 {
		t.Errorf("no change: Moves = %q", got)
	}
}

// A port the change only ignores forwards nothing, so it is gone.
func TestMovesSaysAnIgnoredPortIsGone(t *testing.T) {
	old := config.Project{Routes: []config.Route{{Port: 5173, Role: config.RoleLoopback, Label: "vite"}}}
	next := config.Project{Routes: []config.Route{{Port: 5173, Role: config.RoleIgnore}}}
	if got, want := strings.Join(Moves(old, next), "|"), "port 5173 (vite) → removed"; got != want {
		t.Errorf("Moves = %q, want %q", got, want)
	}
}
