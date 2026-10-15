package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// errNoLongerConfigured is the error of an edit of a project or server that
// has left the config file.
func errNoLongerConfigured(name string) error {
	return fmt.Errorf("%s is no longer in the config", name)
}

// editedMsg is the outcome of an edit of the config file made from a panel.
type editedMsg struct {
	err  error
	note string // logged when the edit was saved
}

// editConfig saves an edit of the config file in the background; when it
// is saved the config is applied.
func (m *Model) editConfig(note string, fn func(cfg *config.Config) error) tea.Cmd {
	b := m.b
	return m.work.do(func(context.Context) tea.Msg {
		return editedMsg{b.EditConfig(fn), note}
	})
}

// routesKey handles a key while the project panel's routes have the focus,
// and reports whether it was one of theirs.
func (m *Model) routesKey(s string) (tea.Cmd, bool) {
	_, name := m.selected()
	p, ok := m.cfg.Project(name)
	if !ok {
		m.routeFocus = false
		return nil, false
	}
	switch s {
	case "tab", "esc":
		m.routeFocus, m.panelErr = false, ""
	case "up", "k":
		m.routeRow = max(m.routeRow-1, 0)
	case "down", "j":
		m.routeRow = min(m.routeRow+1, len(p.Routes)-1)
	case "space":
		return m.cycleRole(*p), true
	case "w":
		return m.makeWeb(*p), true
	case "y":
		return m.copyAdvice(p.Name), true
	default:
		return nil, false
	}
	return nil, true
}

// cycleRole switches the selected route between loopback and ignore, and
// saves it. A project has exactly one web route at all times, so the web
// route is changed with w (makeWeb), not here; on this machine loopback
// means nothing, so a route there is web or ignored.
func (m *Model) cycleRole(p config.Project) tea.Cmd {
	if m.routeRow >= len(p.Routes) {
		return nil
	}
	r := p.Routes[m.routeRow]
	switch {
	case r.Role == config.RoleWeb:
		m.panelErr = "this is the web route; select another route and press w to make it web"
		return nil
	case p.Server == config.LocalServer:
		m.panelErr = "on this machine a route is web or ignored; press w to make it web"
		return nil
	}
	role := config.RoleLoopback
	if r.Role == config.RoleLoopback {
		role = config.RoleIgnore
	}
	return m.setRole(p.Name, r.Port, role)
}

// makeWeb makes the selected route the web route and saves it; the route
// that was web becomes ignore.
func (m *Model) makeWeb(p config.Project) tea.Cmd {
	if m.routeRow >= len(p.Routes) || p.Routes[m.routeRow].Role == config.RoleWeb {
		return nil
	}
	return m.setRole(p.Name, p.Routes[m.routeRow].Port, config.RoleWeb)
}

// setRole saves the role of a project's route on port. A route made web
// takes the place of the one that was.
func (m *Model) setRole(name string, port int, role config.Role) tea.Cmd {
	m.panelErr = ""
	return m.editConfig("", func(cfg *config.Config) error {
		q, ok := cfg.Project(name)
		if !ok {
			return errNoLongerConfigured(name)
		}
		for j := range q.Routes {
			if role == config.RoleWeb && q.Routes[j].Role == config.RoleWeb {
				q.Routes[j].Role = config.RoleIgnore
			}
			if q.Routes[j].Port == port {
				q.Routes[j].Role = role
			}
		}
		return nil
	})
}
