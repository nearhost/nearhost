package nearhostrc

import (
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func exportFixture() (config.Project, PortVars, map[string]string) {
	p := config.Project{
		Name: "app", Server: config.LocalServer, Alias: "app.local.x.io", Enabled: true,
		Routes: []config.Route{
			{Port: 8220, Role: config.RoleWeb, Hosts: []string{"admin.local.x.io"}},
			{Port: 9000, Role: config.RoleWeb, Label: "minio", Hosts: []string{"files.local.x.io"}},
			{Port: 5432, Role: config.RoleIgnore},
		},
		Nearhostrc: &config.RcSnapshot{DockerClients: true},
	}
	vars := PortVars{8220: {"APP_PORT"}, 9000: {"FORWARD_MINIO_PORT"}}
	env := map[string]string{"APP_PORT": "8220", "FORWARD_MINIO_PORT": "9000"}
	return p, vars, env
}

func TestExportWritesLayout(t *testing.T) {
	p, vars, env := exportFixture()
	got, snap, err := Export(p, "app", vars, env)
	if err != nil {
		t.Fatal(err)
	}
	want := `# Written by nearhost export. Safe to edit and commit; see https://nearhost.dev/docs for the format.
version: 1
alias: app.local.x.io
routes:
  - port: ${APP_PORT:-8220}
    hosts: [admin.local.x.io]
  - port: ${FORWARD_MINIO_PORT:-9000}
    label: minio
    hosts: [files.local.x.io]
  - port: 5432
    role: ignore
docker_clients: true
`
	if string(got) != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
	if snap == nil || !snap.DockerClients || snap.Alias != "app.local.x.io" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestExportQuotesWildcardHosts(t *testing.T) {
	p := config.Project{Name: "app", Server: config.LocalServer, Alias: "app.test", Routes: []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"*.status.local.x.io"}},
	}}
	got, _, err := Export(p, "app", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `hosts: ["*.status.local.x.io"]`) {
		t.Fatalf("output:\n%s", got)
	}
}

func TestExportOmitsDefaultAlias(t *testing.T) {
	p := config.Project{Name: "myapp", Server: config.LocalServer, Alias: "myapp.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}}
	got, _, err := Export(p, "myapp", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "alias:") {
		t.Fatalf("output:\n%s", got)
	}
	got, _, err = Export(p, "other", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "alias: myapp.test\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestExportWritesAliasVariable(t *testing.T) {
	p, vars, env := exportFixture()
	env["SITE_HOST"] = "app.local.x.io"
	got, _, err := Export(p, "app", vars, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "alias: ${SITE_HOST:-app.local.x.io}\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestExportWritesBarePortWhenVariableDisagrees(t *testing.T) {
	p, vars, env := exportFixture()
	env["APP_PORT"] = "8221"
	got, _, err := Export(p, "app", vars, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "  - port: 8220\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestExportedFileFillsForAnotherEnv(t *testing.T) {
	p, vars, env := exportFixture()
	got, _, err := Export(p, "app", vars, env)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(got, map[string]string{"APP_PORT": "8300"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Routes[0].Port != 8300 {
		t.Fatalf("port = %d", f.Routes[0].Port)
	}
}

func TestExportRoundTripsVariedProjects(t *testing.T) {
	web := func(port int, label string, hosts ...string) config.Route {
		return config.Route{Port: port, Role: config.RoleWeb, Label: label, Hosts: hosts}
	}
	tests := []struct {
		name string
		p    config.Project
		vars bool
	}{
		{"no hosts", config.Project{Alias: "a.test", Routes: []config.Route{web(80, "")}}, false},
		{"patterns", config.Project{Alias: "a.test", Routes: []config.Route{web(80, "", "*.a.local.x.io", "a.local.x.io")}}, false},
		{"labels", config.Project{Alias: "a.test", Routes: []config.Route{web(80, "My app #1"), {Port: 5432, Role: config.RoleIgnore, Label: "db: main"}}}, false},
		{"several web", config.Project{Alias: "a.test", Routes: []config.Route{web(80, ""), web(81, "b", "b.test"), web(82, "c", "c.test", "d.test")}}, false},
		{"loopback on remote", config.Project{Server: "box", Alias: "a.test", Routes: []config.Route{web(80, ""), {Port: 5432, Role: config.RoleLoopback}}}, false},
		{"custom alias", config.Project{Alias: "app.local.x.io", Routes: []config.Route{web(80, "")}}, false},
		{"variable port and alias", config.Project{Alias: "app.local.x.io", Routes: []config.Route{web(80, "")}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars, env := PortVars(nil), map[string]string(nil)
			if tt.vars {
				vars, env = PortVars{80: {"WEB_PORT"}}, map[string]string{"WEB_PORT": "80", "SITE_HOST": "app.local.x.io"}
			}
			p := tt.p
			p.Name = "a"
			if p.Server == "" {
				p.Server = config.LocalServer
			}
			got, snap, err := Export(p, "a", vars, env)
			if err != nil {
				t.Fatal(err)
			}
			f, err := Parse(got, env)
			if err != nil {
				t.Fatal(err)
			}
			if snap.SHA256 != f.SHA256 {
				t.Fatalf("snapshot hash %s, parse hash %s", snap.SHA256, f.SHA256)
			}
		})
	}
}

func TestExportRefusesWhatItCannotRepresent(t *testing.T) {
	p := config.Project{Name: "a", Server: config.LocalServer, Alias: "a.test", Routes: []config.Route{
		{Port: 80, Role: config.RoleWeb},
		{Port: 5432, Role: config.RoleIgnore, Label: "two\nlines"},
	}}
	got, snap, err := Export(p, "a", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "5432") || got != nil || snap != nil {
		t.Fatalf("got %q, %v, %v", got, snap, err)
	}
}

func TestExportUsesUnsetPortVariable(t *testing.T) {
	p := config.Project{Name: "a", Server: config.LocalServer, Alias: "a.test", Routes: []config.Route{{Port: 8220, Role: config.RoleWeb}}}
	got, _, err := Export(p, "a", PortVars{8220: {"APP_PORT"}}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "port: ${APP_PORT:-8220}\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestExportPicksAlphabeticallyFirstAliasVariable(t *testing.T) {
	p := config.Project{Name: "a", Server: config.LocalServer, Alias: "x.local.io", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}}
	got, _, err := Export(p, "a", nil, map[string]string{"B_HOST": "x.local.io", "A_HOST": "x.local.io"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "alias: ${A_HOST:-x.local.io}\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestExportRefusesLabelWithVariableSyntax(t *testing.T) {
	p := config.Project{Name: "a", Server: config.LocalServer, Alias: "a.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Label: "cost ${X}"}}}
	got, snap, err := Export(p, "a", nil, nil)
	if err == nil || got != nil || snap != nil {
		t.Fatalf("got %q, %v, %v", got, snap, err)
	}
}

func TestExportAliasVariableMustEndInHost(t *testing.T) {
	p := config.Project{Name: "a", Server: config.LocalServer, Alias: "app.x.io", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"session domain", map[string]string{"SESSION_DOMAIN": "app.x.io"}, "alias: app.x.io\n"},
		{"host suffix", map[string]string{"SITE_HOST": "app.x.io"}, "alias: ${SITE_HOST:-app.x.io}\n"},
		{"first of two hosts", map[string]string{"B_HOST": "app.x.io", "A_HOST": "app.x.io"}, "alias: ${A_HOST:-app.x.io}\n"},
		{"domain suffix", map[string]string{"APP_DOMAIN": "app.x.io"}, "alias: app.x.io\n"},
		{"not exactly the name", map[string]string{"APP_HOST": "app.x.io,other"}, "alias: app.x.io\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := Export(p, "a", nil, c.env)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), c.want) {
				t.Fatalf("output:\n%s\nwant %q", got, c.want)
			}
		})
	}
}
