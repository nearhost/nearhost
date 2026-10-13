package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func TestLsPrintsProjectsWithServerAliasStateAndRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, path, &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini.lan"}},
		Projects: []config.Project{
			{Name: "myapp", Server: config.LocalServer, Alias: "myapp.test", Enabled: true, Routes: []config.Route{
				{Port: 8080, Role: config.RoleWeb}, {Port: 5174, Role: config.RoleLoopback}, {Port: 3307, Role: config.RoleIgnore},
			}},
			{Name: "api", Server: "mini", Alias: "api.test", Enabled: false, Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}},
		},
	})
	out, err := execute(t, newLsCmd(), "--config", path, "ls")
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"NAME   SERVER  ALIAS       STATE     ROUTES\n" +
		"myapp  local   myapp.test  enabled   web :8080, loopback :5174\n" +
		"api    mini    api.test    disabled  web :80\n"
	if out != want {
		t.Fatalf("ls printed:\n%s\nwant:\n%s", out, want)
	}
}

func TestLsWithNoProjectsSaysHowToAddOne(t *testing.T) {
	out, err := execute(t, newLsCmd(), "--config", filepath.Join(t.TempDir(), "none.yaml"), "ls")
	if err != nil {
		t.Fatal(err)
	}
	if out != "No projects yet. Add one with `nearhost add <path>`.\n" {
		t.Fatalf("empty ls printed %q", out)
	}
}

func TestLsFailsOnAnInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("projects: [not, a, project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newLsCmd(), "--config", path, "ls")
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("expected a parse error naming the file, got %v\n%s", err, out)
	}
}

func TestLsRejectsPositionalArguments(t *testing.T) {
	// No config file, which ls accepts: only the argument can be at fault.
	path := filepath.Join(t.TempDir(), "none.yaml")
	if out, err := execute(t, newLsCmd(), "--config", path, "ls", "extra"); err == nil {
		t.Fatalf("ls must reject positional arguments; it printed:\n%s", out)
	}
}
