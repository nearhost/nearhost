package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
)

// The mark on a route no longer found is shown, never saved.
func TestNotFoundMarkIsNotSaved(t *testing.T) {
	m, f, _ := editModel(t)
	wantScreen(t, m, "3306   ignore    db (not found now)")
	press(m, "enter")
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	p, _ := f.cfg.Project("myapp")
	for _, r := range p.Routes {
		if r.Port == 3306 && r.Label != "db" {
			t.Errorf("saved label %q", r.Label)
		}
	}
}

// Editing keeps the project's path: after discovery fails, the path step
// only reads the project again.
func TestEditKeepsThePathAfterADiscoveryError(t *testing.T) {
	f := newFake(t, sampleConfig())
	files := &fakeFiles{dirs: map[string]bool{}, drafts: map[string]Draft{}}
	f.forms.files = files
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	settle(m, press(m, "e"))
	wantScreen(t, m, "/srv/myapp: no such directory", "enter read again · ctrl+r reconnect · esc back")
	typeText(m, "/elsewhere")
	files.dirs["/srv/myapp"] = true
	files.drafts["/srv/myapp"] = Draft{Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Label: "web"}}}
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit myapp · routes")
	pf := m.form.(*projectForm)
	if pf.resolved != "/srv/myapp" || pf.path.value != "/srv/myapp" {
		t.Errorf("path became %q / %q", pf.resolved, pf.path.value)
	}
}

// Advice that could not be read is read again, after a while.
func TestAdviceIsRetriedAfterAFailure(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.edit.adviceErr = errors.New("ssh mini.lan failed")
	m := sized(f)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.selectRow(rowKey(rowProject, "myapp"))
	_, read := m.Update(nil)
	run(m, read)
	if _, again := m.Update(nil); again != nil {
		t.Fatal("advice was read again at once")
	}
	now = now.Add(adviceRetry + time.Second)
	f.edit.adviceErr = nil
	_, again := m.Update(nil)
	if again == nil {
		t.Fatal("advice was not read again")
	}
	run(m, again)
	if len(f.edit.adviceAsked) != 2 {
		t.Errorf("asked %d times", len(f.edit.adviceAsked))
	}
}

// A role change waits for the last one to be applied, so it starts from the
// project as it is now.
func TestRoleChangesWaitForTheLastOne(t *testing.T) {
	m, f := routesModel(t)
	press(m, "down")
	reload := run(m, press(m, "space")) // 5173 to ignore; the config is applied next
	if reload == nil || f.edit.edits != 1 {
		t.Fatalf("edits %d", f.edit.edits)
	}
	run(m, press(m, "space")) // m.cfg still says loopback
	if f.edit.edits != 1 {
		t.Fatalf("a second change went ahead before the first was applied")
	}
	m.Update(appliedMsg{})
	run(m, press(m, "space"))
	if f.edit.edits != 2 {
		t.Errorf("edits %d after the first was applied", f.edit.edits)
	}
}
