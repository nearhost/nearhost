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
