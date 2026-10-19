package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// changeFake is the part of the fake backend the review of a .nearhostrc
// change uses.
type changeFake struct {
	rechecked []string
	accepted  []string
	declined  []string
	acceptErr error
	saved     int // changes AcceptChange took
}

func (f *fakeBackend) Recheck(_ context.Context, project string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.change.rechecked = append(f.change.rechecked, project)
	return nil
}

// AcceptChange refuses a change other than the one pending now, as the real
// one does; it records "<project> <sha>" when it is asked.
func (f *fakeBackend) AcceptChange(project, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.change.accepted = append(f.change.accepted, project+" "+sha)
	if p := f.states[project].Pending; p == nil || p.SHA256 != sha {
		return errors.New("the .nearhostrc changed again; review it again")
	}
	if f.change.acceptErr != nil {
		return f.change.acceptErr
	}
	f.change.saved++
	return nil
}

func (f *fakeBackend) DeclineChange(project, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.change.declined = append(f.change.declined, project+" "+sha)
}

// pendingModel is a model with the project dd selected, whose .nearhostrc
// asks for app.local.dd.io on port 8220, where old.local.dd.io was served on
// port 80.
func pendingModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	cfg := sampleConfig()
	cfg.Projects = append(cfg.Projects, config.Project{
		Name: "dd", Server: config.LocalServer, Path: "/code/dd", Alias: "dd.test", Enabled: true,
		Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"old.local.dd.io"}}},
	})
	f := newFake(t, cfg)
	next := cfg.Projects[3]
	next.Routes = []config.Route{{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "old.local.dd.io"}}}
	f.setState("dd", engine.ProjectStatus{State: engine.StateRunning, Pending: &engine.Change{
		SHA256: "new", Project: next,
		Items: []nearhostrc.Item{{Port: 8220, Host: "app.local.dd.io", Why: "a real domain"}},
	}})
	f.forms.public = map[string]string{"app.local.dd.io": "203.0.113.7"}
	m := sized(f)
	m.selectRow(rowKey(rowProject, "dd"))
	return m, f
}

func TestReviewScreenApprovesAndDeclines(t *testing.T) {
	m, f := pendingModel(t)
	wantScreen(t, m, ".nearhostrc asks for 1 change that needs approval · a to review")
	settle(m, press(m, "a"))
	wantScreen(t, m, "app.local.dd.io on port 8220 (a real domain)", "resolves to 203.0.113.7",
		"old.local.dd.io port 80 → 8220", "y approve · n decline · esc later")
	settle(m, press(m, "y"))
	if len(f.change.accepted) != 1 || f.change.accepted[0] != "dd new" || f.change.saved != 1 || m.form != nil {
		t.Fatalf("accepted %v, form %T", f.change.accepted, m.form)
	}

	// An approval refused stays on the screen, saying why.
	f.change.acceptErr = errBoom
	settle(m, press(m, "a"))
	settle(m, press(m, "y"))
	if m.form == nil {
		t.Fatal("a refused approval closed the screen")
	}
	wantScreen(t, m, "Boom")
	settle(m, press(m, "esc"))
	if m.form != nil || len(f.change.declined) != 0 {
		t.Fatalf("esc: form %T, declined %v", m.form, f.change.declined)
	}

	settle(m, press(m, "a"))
	settle(m, press(m, "n"))
	if len(f.change.declined) != 1 || f.change.declined[0] != "dd new" || m.form != nil {
		t.Fatalf("declined %v, form %T", f.change.declined, m.form)
	}
}

// y approves the change the screen shows: one the file replaced while it was
// open is refused, on the screen, and nothing is saved.
func TestReviewScreenRefusesAChangeItDidNotShow(t *testing.T) {
	m, f := pendingModel(t)
	settle(m, press(m, "a"))
	st := f.Status("dd")
	newer := *st.Pending
	newer.SHA256 = "newer"
	st.Pending = &newer
	f.setState("dd", st)
	settle(m, press(m, "y"))
	if m.form == nil || f.change.saved != 0 || len(f.change.accepted) != 1 || f.change.accepted[0] != "dd new" {
		t.Fatalf("form %T, saved %d, accepted %v", m.form, f.change.saved, f.change.accepted)
	}
	wantScreen(t, m, "The .nearhostrc changed again; review it again")
}

// Two hosts are counted as hosts, and a is nothing on a project with no
// change waiting.
func TestPendingLineCountsHosts(t *testing.T) {
	m, f := pendingModel(t)
	st := f.Status("dd")
	st.Pending.Items = append(st.Pending.Items, nearhostrc.Item{Port: 8220, Host: "*.local.dd.io", Why: "a wildcard"})
	f.setState("dd", st)
	wantScreen(t, m, ".nearhostrc asks for 2 changes that need approval · a to review")

	st.Pending, st.Notice = nil, ".nearhostrc: version 2 is newer than this Nearhost understands"
	f.setState("dd", st)
	if strings.Contains(screen(m), "need approval") {
		t.Errorf("a project with nothing pending asks for approval:\n%s", screen(m))
	}
	wantScreen(t, m, "version 2 is newer than this Nearhost understands")
	press(m, "a")
	if m.form != nil {
		t.Errorf("a opened %T with nothing pending", m.form)
	}
}

func TestEnablingRefusesAClash(t *testing.T) {
	cfg := sampleConfig()
	cfg.Projects[0].Routes[0].Hosts = []string{"x.local.example.io"} // website, enabled
	cfg.Projects[2].Routes[0].Hosts = []string{"x.local.example.io"} // old, disabled
	f := newFake(t, cfg)
	m := sized(f)
	m.selectRow(rowKey(rowProject, "old"))
	run(m, press(m, "enter"))
	wantScreen(t, m, `Host x.local.example.io is already served by project "website"`)
	if p, _ := f.cfg.Project("old"); len(f.started) != 0 || p.Enabled {
		t.Fatalf("started %v, enabled %v", f.started, p.Enabled)
	}
}

func TestRecheckKeyOnAProject(t *testing.T) {
	m, f := pendingModel(t)
	run(m, press(m, "r"))
	if len(f.change.rechecked) != 1 || f.change.rechecked[0] != "dd" {
		t.Fatalf("rechecked %v", f.change.rechecked)
	}
	if !strings.Contains(logText(m, rowKey(rowProject, "dd")), "re-read .nearhostrc") {
		t.Errorf("log:\n%s", logText(m, rowKey(rowProject, "dd")))
	}
	m.selectRow(rowKey(rowServer, "mini"))
	run(m, press(m, "r"))
	if len(f.edit.reconnected) != 1 || f.edit.reconnected[0] != "mini" || len(f.change.rechecked) != 1 {
		t.Fatalf("reconnected %v, rechecked %v", f.edit.reconnected, f.change.rechecked)
	}
}

// The review lists a host moved or gone, and a route gone with its hosts.
func TestMovesSaysWhatGoes(t *testing.T) {
	old := config.Project{Routes: []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"a.local.dd.io", "b.local.dd.io"}},
		{Port: 5173, Role: config.RoleLoopback, Label: "vite"},
		{Port: 3306, Role: config.RoleIgnore},
	}}
	next := config.Project{Routes: []config.Route{{Port: 8080, Role: config.RoleWeb, Hosts: []string{"a.local.dd.io"}}}}
	got := strings.Join(moves(old, next), "|")
	if want := "a.local.dd.io port 80 → 8080|b.local.dd.io port 80 → removed|port 5173 (vite) → removed"; got != want {
		t.Errorf("moves = %q, want %q", got, want)
	}
}

// A change that only asks for docker_clients is reviewed like one for hosts,
// without a lookup of an address it does not have.
func TestChangeScreenShowsDockerClients(t *testing.T) {
	m, f := pendingModel(t)
	st := f.Status("dd")
	st.Pending.Items = []nearhostrc.Item{{Option: nearhostrc.OptionDockerClients, Why: "lets local Docker containers reach this project"}}
	f.setState("dd", st)
	f.forms.public = map[string]string{"": "203.0.113.7"}
	wantScreen(t, m, ".nearhostrc asks for 1 change that needs approval · a to review")
	settle(m, press(m, "a"))
	wantScreen(t, m, "docker_clients (lets local Docker containers reach this project)")
	if s := screen(m); strings.Contains(s, "on port 0") || strings.Contains(s, "resolves to") {
		t.Errorf("an option is shown as a host:\n%s", s)
	}
}
