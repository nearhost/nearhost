package tui

import (
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// An edit that renames a project of a pair the config already had is saved;
// one that brings in a new host under the other project's pattern is not.
func TestEditKeepsAPairTheConfigHad(t *testing.T) {
	web := func(port int, hosts ...string) []config.Route {
		return []config.Route{{Port: port, Role: config.RoleWeb, Hosts: hosts}}
	}
	cfg := &config.Config{Projects: []config.Project{
		{Name: "a", Alias: "myapp.test", Enabled: true, Routes: web(80, "*.myapp.test")},
		{Name: "b", Alias: "b.test", Enabled: true, Routes: web(81, "api.myapp.test")},
	}}
	before := append([]config.Project(nil), cfg.Projects...)
	renamed := cfg.Projects[1]
	renamed.Name = "b2"
	cfg.Projects[1] = renamed
	if err := hostClash(cfg, renamed, before...); err != nil {
		t.Errorf("renaming b: %v", err)
	}
	grown := renamed
	grown.Routes = web(81, "api.myapp.test", "www.myapp.test")
	cfg.Projects[1] = grown
	if err := hostClash(cfg, grown, before...); err == nil || !strings.Contains(err.Error(), `host www.myapp.test of project "b2" falls under *.myapp.test`) {
		t.Errorf("adding www.myapp.test: %v", err)
	}
}
