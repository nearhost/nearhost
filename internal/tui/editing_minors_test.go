package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// footerX is the column of the footer action with key.
func footerX(t *testing.T, m *Model, key string) int {
	t.Helper()
	for x := 0; x < m.width; x++ {
		if m.footerKeyAt(x) == key {
			return x
		}
	}
	t.Fatalf("no %q in the footer", key)
	return 0
}

// Footer actions clicked while the routes have the focus act on the routes.
func TestFooterClicksActOnFocusedRoutes(t *testing.T) {
	m, f := routesModel(t)
	press(m, "down")
	run(m, m.click(tea.Mouse{X: footerX(t, m, "space"), Y: 29, Button: tea.MouseLeft}))
	if roleOf(f.cfg, "myapp", 5173) != "ignore" {
		t.Errorf("clicking space left 5173 %s", roleOf(f.cfg, "myapp", 5173))
	}
	m.click(tea.Mouse{X: footerX(t, m, "tab"), Y: 29, Button: tea.MouseLeft})
	if m.routeFocus {
		t.Error("clicking tab done left the focus on the routes")
	}
}

// Advice is read once the selection rests on a project, not for each
// project the cursor passes.
func TestAdviceWaitsForTheSelectionToRest(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.selectRow(rowKey(rowProject, "website"))
	_, wait := m.Update(nil)
	m.selectRow(rowKey(rowProject, "myapp")) // moved on before the wait ended
	wait = run(m, wait)                      // website's wait ends; myapp's begins
	if len(f.edit.adviceAsked) != 0 {
		t.Fatalf("advice read for a project passed over: %v", f.edit.adviceAsked)
	}
	run(m, run(m, wait))
	if len(f.edit.adviceAsked) != 1 || f.edit.adviceAsked[0] != "myapp" {
		t.Errorf("asked %v", f.edit.adviceAsked)
	}
}

// A refusal that arrives after the selection moved on is logged on its own
// row, not shown on the row selected now.
func TestRefusalAfterMovingOnStaysWithItsRow(t *testing.T) {
	m, f := routesModel(t)
	press(m, "down")
	f.edit.editErr = errors.New("permission denied")
	edit := press(m, "w")
	m.selectRow(rowKey(rowProject, "website"))
	run(m, edit)
	if m.panelErr != "" {
		t.Errorf("the refusal shows on website: %q", m.panelErr)
	}
	m.selectRow(rowKey(rowProject, "myapp"))
	wantScreen(t, m, "Permission denied")
}

// s on the first-run screen opens Home.
func TestFirstRunSkipOpensHome(t *testing.T) {
	m, _ := started(t, false)
	m.sel = rowKey(rowProject, "myapp")
	settle(m, press(m, "s"))
	if m.sel != homeKey {
		t.Errorf("selected %q after skipping", m.sel)
	}
}

// The first-run screen is offered at start or not at all: it does not open
// later, after a form that was open at start has closed.
func TestFirstRunDoesNotOpenLater(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.trusted = false
	m := sized(f)
	settle(m, press(m, "s")) // the add-server form is open when the engine starts
	m.Update(startedMsg{})
	m.Update(machineMsg{caTrusted: false})
	settle(m, press(m, "esc"))
	m.Update(machineMsg{caTrusted: false})
	if m.form != nil {
		t.Errorf("%T opened after the form closed", m.form)
	}
	if !strings.Contains(screen(m), "not trusted") {
		t.Errorf("Home does not say the CA is untrusted:\n%s", screen(m))
	}
}
