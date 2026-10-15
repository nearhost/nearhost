package tui

import (
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// editModel is a model with the form open to edit myapp, whose files on
// mini now hold ports 80 and 8025 (3306 and 5173 are no longer found).
func editModel(t *testing.T) (*Model, *fakeBackend, *fakeFiles) {
	t.Helper()
	f := newFake(t, sampleConfig())
	files := &fakeFiles{home: "/home/dev", dirs: map[string]bool{"/srv/myapp": true}, drafts: map[string]Draft{
		"/srv/myapp": {Routes: []config.Route{
			{Port: 80, Role: config.RoleWeb, Label: "app"},
			{Port: 8025, Role: config.RoleWeb, Label: "mail"},
		}},
	}}
	f.forms.files = files
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	settle(m, press(m, "e"))
	return m, f, files
}

func TestEditProjectKeepsSavedRoutes(t *testing.T) {
	m, _, _ := editModel(t)
	wantScreen(t, m, "Edit myapp · routes",
		"80     web       web",  // saved role and label kept
		"8025   ignore    mail", // new: does not take the web route
		"5173   loopback  vite (VITE_PORT) (not found now)",
		"3306   ignore    db (not found now)")
}

func TestEditProjectRenamesAndSaves(t *testing.T) {
	m, f, files := editModel(t)
	press(m, "enter") // routes
	wantScreen(t, m, "Edit myapp · name", "name      myapp▏", "alias     myapp.test")
	// Its own name and alias are not taken by itself.
	f.forms.nameErr = errors.New(`a project called "myapp" already exists, on mini`)
	f.forms.aliasErr = errors.New(`alias myapp.test is already used by project "myapp"`)
	typeText(m, "2")
	press(m, "ctrl+u")
	typeText(m, "myapp")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Edit myapp · confirm", "enter save · esc back")
	press(m, "esc")
	press(m, "ctrl+u")
	typeText(m, "shop")
	f.forms.nameErr, f.forms.aliasErr = nil, nil
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	p, ok := f.cfg.Project("shop")
	if !ok || p.Alias != "shop.test" || p.Server != "mini" || p.Path != "/srv/myapp" || len(p.Routes) != 4 {
		t.Fatalf("saved %+v %v", p, ok)
	}
	if _, still := f.cfg.Project("myapp"); still {
		t.Error("the old name is still configured")
	}
	if f.cfg.Projects[1].Name != "shop" {
		t.Errorf("the project moved: %+v", f.cfg.Projects)
	}
	if m.form != nil || m.sel != rowKey(rowProject, "shop") || files.closed != 1 {
		t.Errorf("form %T, selected %q, files closed %d", m.form, m.sel, files.closed)
	}
	wantScreen(t, m, "Saved shop.")
}

func TestEditProjectCancels(t *testing.T) {
	m, f, files := editModel(t)
	settle(m, press(m, "esc"))
	if m.form != nil || files.closed != 1 || f.edit.edits != 0 {
		t.Errorf("form %T, files closed %d, edits %d", m.form, files.closed, f.edit.edits)
	}
}

func TestRemoveProject(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowProject, "myapp"))
	press(m, "d")
	wantScreen(t, m, "Remove myapp?", "It stops now and leaves the config.", "y remove · n keep")
	settle(m, press(m, "n"))
	if m.form != nil || len(f.cfg.Projects) != 3 {
		t.Fatalf("n: form %T, %d projects", m.form, len(f.cfg.Projects))
	}
	press(m, "d")
	f.edit.editErr = errors.New("write config.yaml: permission denied")
	settle(m, press(m, "y"))
	wantScreen(t, m, "Write config.yaml: permission denied", "y remove · n keep")
	f.edit.editErr = nil
	settle(m, press(m, "y"))
	if _, ok := f.cfg.Project("myapp"); ok || m.form != nil {
		t.Errorf("myapp still configured, or form %T open", m.form)
	}
	if m.sel != rowKey(rowServer, "mini") {
		t.Errorf("selected %q", m.sel)
	}
	wantScreen(t, m, "Removed myapp.")
}
