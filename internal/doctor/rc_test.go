package doctor

import (
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

type files map[string]string

func (f files) ReadFile(name string) ([]byte, error) {
	if s, ok := f[name]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func byID(t *testing.T, fs []Finding, id string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no %s in %+v", id, fs)
	return Finding{}
}

func ids(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func TestRcFindingsForBrokenFiles(t *testing.T) {
	cases := []struct {
		name  string
		rc    string
		id    string
		kind  FixKind
		line  int
		texts []string
	}{
		{"unknown key", "version: 1\nroutes:\n  bogus: 1\n", "nearhostrc.invalid", FixProject, 3, nil},
		{"unset", "version: 1\nroutes:\n  - port: 80\n    hosts: [${API_HOST}]\n", "nearhostrc.unset-variable", FixProject, 4,
			[]string{"API_HOST", ".env.local or .env", "${API_HOST:-"}},
		{"version", "version: 2\n", "nearhostrc.version", FixManual, 0, []string{"nearhost update"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Check(context.Background(), Input{Files: files{".nearhostrc": c.rc}})
			f := byID(t, got, c.id)
			if f.Severity != SeverityError || f.Fix.Kind != c.kind || f.File != ".nearhostrc" || f.Line != c.line {
				t.Errorf("got %+v", f)
			}
			for _, s := range c.texts {
				if !strings.Contains(f.Fix.Text, s) {
					t.Errorf("fix text %q lacks %q", f.Fix.Text, s)
				}
			}
		})
	}
}

const rcApproved = "version: 1\nroutes:\n  - port: 8000\n    role: web\n    hosts: [app.test]\n"

func approved(t *testing.T) config.Project {
	t.Helper()
	f, _, err := nearhostrc.Read(files{".nearhostrc": rcApproved})
	if err != nil {
		t.Fatal(err)
	}
	return config.Project{
		Name: "app", Server: config.LocalServer, Enabled: true,
		Routes:     []config.Route{{Port: 8000, Role: config.RoleWeb, Hosts: []string{"app.test"}}},
		Nearhostrc: &config.RcSnapshot{SHA256: f.SHA256, Routes: []config.Route{{Port: 8000, Role: config.RoleWeb, Hosts: []string{"app.test"}}}},
	}
}

func TestRcFindingsAgainstTheApprovedSnapshot(t *testing.T) {
	p := approved(t)
	run := func(rc map[string]string) []Finding {
		return Check(context.Background(), Input{Project: p, Added: true, Files: files(rc)})
	}
	if got := run(map[string]string{".nearhostrc": rcApproved}); len(got) != 0 {
		t.Errorf("approved file: %+v", got)
	}

	got := run(map[string]string{".nearhostrc": rcApproved + "  - port: 9000\n    role: web\n    hosts: [shop.example.com]\n"})
	f := byID(t, got, "nearhostrc.pending")
	if f.Severity != SeverityWarning || f.Fix.Kind != FixNearhost || f.Project != "app" || !strings.Contains(f.Message, "shop.example.com") {
		t.Errorf("pending: %+v", f)
	}

	got = run(map[string]string{".nearhostrc": strings.Replace(rcApproved, "8000", "8001", 1)})
	f = byID(t, got, "nearhostrc.not-applied")
	if f.Severity != SeverityWarning || f.Fix.Kind != FixNearhost {
		t.Errorf("not-applied: %+v", f)
	}

	got = run(map[string]string{})
	f = byID(t, got, "nearhostrc.gone")
	if f.Severity != SeverityWarning || f.Fix.Kind != FixProject || f.Project != "app" {
		t.Errorf("gone: %+v", f)
	}
}

func TestRcFindingsForADirectoryNotAdded(t *testing.T) {
	p := config.Project{Name: "app", Server: config.LocalServer, Enabled: true}
	rc := "version: 1\nroutes:\n  - port: 9000\n    role: web\n    hosts: [shop.example.com]\n"
	got := Check(context.Background(), Input{Project: p, Files: files{".nearhostrc": rc}})
	if len(got) != 0 {
		t.Errorf("want none, got %+v", got)
	}
	got = Check(context.Background(), Input{Project: p, Files: files{".nearhostrc": "version: 2\n"}})
	if f := byID(t, got, "nearhostrc.version"); f.Project != "" {
		t.Errorf("project %q", f.Project)
	}

	// A file that parses but that add could not take is reported before add.
	remote := config.Project{Name: "app", Server: "box", Enabled: true}
	got = Check(context.Background(), Input{Project: remote, Files: files{".nearhostrc": rc + "docker_clients: true\n"}})
	if f := byID(t, got, "nearhostrc.invalid"); f.Severity != SeverityError || !strings.Contains(f.Message, "docker_clients works only") ||
		f.Fix.Kind != FixProject || len(got) != 1 {
		t.Errorf("a file add could not take: %+v", got)
	}
}

func TestDriftFindings(t *testing.T) {
	disc := &discovery.Result{Routes: []discovery.Candidate{
		{Port: 5173, Role: config.RoleWeb}, {Port: 8000, Role: config.RoleWeb},
	}}
	p := config.Project{Name: "app", Enabled: true, Routes: []config.Route{
		{Port: 8000, Role: config.RoleWeb}, {Port: 6001, Role: config.RoleWeb},
	}}
	in := Input{Project: p, Added: true, Files: files{}, Discovered: disc}

	got := Check(context.Background(), in)
	u := byID(t, got, "routes.unserved-port")
	if u.Severity != SeverityWarning || u.Fix.Kind != FixNearhost || !strings.Contains(u.Message, "5173") ||
		!strings.Contains(u.Fix.Text, "select it and press e") {
		t.Errorf("unserved: %+v", u)
	}
	s := byID(t, got, "routes.stale-port")
	if s.Severity != SeverityInfo || !strings.Contains(s.Message, "6001") {
		t.Errorf("stale: %+v", s)
	}
	if len(got) != 2 {
		t.Errorf("got %v", ids(got))
	}

	in.Files = files{".nearhostrc": rcApproved}
	for _, f := range Check(context.Background(), in) {
		if strings.HasPrefix(f.ID, "routes.") {
			t.Errorf("with .nearhostrc: %+v", f)
		}
	}

	in.Files, in.Discovered = files{}, nil
	if got := Check(context.Background(), in); len(got) != 0 {
		t.Errorf("no discovery: %+v", got)
	}
}

func TestAdviceAndNotesBecomeFindings(t *testing.T) {
	got := Check(context.Background(), Input{
		Files:      files{},
		Advisories: []discovery.Advisory{{Code: "app-url", Message: "m", Snippet: "s"}},
		Notes:      []string{"n"},
	})
	a := byID(t, got, "advice.app-url")
	if a.Severity != SeverityWarning || a.Fix.Kind != FixProject || a.Message != "m" || a.Fix.Snippet != "s" {
		t.Errorf("advice: %+v", a)
	}
	n := byID(t, got, "presets.note")
	if n.Severity != SeverityInfo || n.Fix.Kind != FixManual || n.Message != "n" {
		t.Errorf("note: %+v", n)
	}
}

func TestCheckSortsAndNeverReturnsNil(t *testing.T) {
	got := Check(context.Background(), Input{Files: files{}})
	if got == nil {
		t.Fatal("nil")
	}
	if b, _ := json.Marshal(got); string(b) != "[]" {
		t.Errorf("json %s", b)
	}

	got = Check(context.Background(), Input{
		Files:      files{".nearhostrc": "version: 2\n"},
		Advisories: []discovery.Advisory{{Code: "b", Message: "m"}},
		Notes:      []string{"n"},
	})
	want := []string{"nearhostrc.version", "advice.b", "presets.note"}
	if g := ids(got); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", g, want)
	}
}

func TestDriftUnservedHost(t *testing.T) {
	disc := &discovery.Result{Routes: []discovery.Candidate{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
	}}
	project := func(routes ...config.Route) config.Project {
		return config.Project{Name: "app", Alias: "a.test", Enabled: true, Routes: routes}
	}
	web80 := config.Route{Port: 80, Role: config.RoleWeb}

	in := Input{Project: project(web80), Added: true, Files: files{}, Discovered: disc}
	got := Check(context.Background(), in)
	f := byID(t, got, "routes.unserved-host")
	if len(got) != 1 || f.Severity != SeverityWarning ||
		f.Message != "app.x.io is named by the project's settings for port 80, but Nearhost doesn't serve it" ||
		f.Fix.Kind != FixNearhost ||
		f.Fix.Text != "Edit the project in Nearhost: select it and press e. The form adds app.x.io for you to approve." {
		t.Errorf("unserved host: %+v", got)
	}

	web8080 := config.Route{Port: 8080, Role: config.RoleWeb, Hosts: []string{"app.x.io"}}
	served := map[string][]config.Route{
		"exact":      {{Port: 80, Role: config.RoleWeb, Hosts: []string{"APP.x.io"}}},
		"pattern":    {{Port: 80, Role: config.RoleWeb, Hosts: []string{"*.x.io"}}},
		"other port": {web80, web8080},
	}
	for name, routes := range served {
		in := Input{Project: project(routes...), Added: true, Files: files{}, Discovered: disc}
		for _, f := range Check(context.Background(), in) {
			if f.ID == "routes.unserved-host" {
				t.Errorf("%s serves the host: %+v", name, f)
			}
		}
	}

	in = Input{Project: project(web80), Added: true, Files: files{".nearhostrc": rcApproved}, Discovered: disc}
	for _, f := range Check(context.Background(), in) {
		if strings.HasPrefix(f.ID, "routes.") {
			t.Errorf("with .nearhostrc: %+v", f)
		}
	}

	disc8080 := &discovery.Result{Routes: []discovery.Candidate{
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
	}}
	in = Input{Project: project(web80), Added: true, Files: files{}, Discovered: disc8080}
	got = Check(context.Background(), in)
	byID(t, got, "routes.unserved-port")
	for _, f := range got {
		if f.ID == "routes.unserved-host" {
			t.Errorf("unserved port should not also report the host: %+v", f)
		}
	}
}

// A host the settings name for a port the project saved as ignore or
// loopback is not served either, and the fix makes the port web first.
func TestDriftUnservedHostOnAPortNotServedAsWeb(t *testing.T) {
	disc := &discovery.Result{Routes: []discovery.Candidate{
		{Port: 8080, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
	}}
	for _, role := range []config.Role{config.RoleIgnore, config.RoleLoopback} {
		p := config.Project{Name: "app", Alias: "a.test", Enabled: true, Routes: []config.Route{
			{Port: 80, Role: config.RoleWeb}, {Port: 8080, Role: role},
		}}
		got := Check(context.Background(), Input{Project: p, Added: true, Files: files{}, Discovered: disc})
		f := byID(t, got, "routes.unserved-host")
		if f.Severity != SeverityWarning ||
			f.Message != "app.x.io is named by the project's settings for port 8080, but Nearhost doesn't serve it" ||
			f.Fix.Kind != FixNearhost ||
			f.Fix.Text != "Edit the project in Nearhost: select it and press e, make port 8080 a web route, and add app.x.io." {
			t.Errorf("%s: %+v", role, got)
		}
		for _, f := range got {
			if f.ID == "routes.unserved-port" {
				t.Errorf("%s: the port has a route: %+v", role, f)
			}
		}
	}
}
