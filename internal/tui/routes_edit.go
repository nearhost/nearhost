package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
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
	key  string // the row the edit was made from
}

// editConfig saves an edit of the config file, made from the row with key,
// in the background; when it is saved the config is applied.
func (m *Model) editConfig(key, note string, fn func(cfg *config.Config) error) tea.Cmd {
	b := m.b
	return m.work.do(func(context.Context) tea.Msg {
		return editedMsg{b.EditConfig(fn), note, key}
	})
}

// refused shows why a change made from the row with key was refused: in the
// panel when that row is still selected, otherwise in its log.
func (m *Model) refused(key string, err error) {
	if key == m.sel {
		m.panelErr = err.Error()
		return
	}
	m.logs.noteOn(key, sentence(err.Error()))
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
	if m.editing {
		return nil // the last change is not applied yet
	}
	m.editing, m.panelErr = true, ""
	return m.editConfig(rowKey(rowProject, name), "", func(cfg *config.Config) error {
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

// reconnectedMsg is the outcome of asking for a server to reconnect.
type reconnectedMsg struct {
	name string
	err  error
}

// connectsOnC reports whether c connects the server called name, rather
// than dropping its connection.
func (m *Model) connectsOnC(name string) bool {
	h := m.b.ServerHealth(name)
	return h.Held || h.Status == transport.StatusDisconnected || h.Status == transport.StatusAuthFailed
}

// toggleConnection drops a server's connection and holds it closed, or
// connects a server that is down.
func (m *Model) toggleConnection(name string) tea.Cmd {
	m.panelErr = ""
	b, connect := m.b, m.connectsOnC(name)
	note := "Dropping the connection to " + name + "."
	if connect {
		note = "Connecting " + name + "."
	}
	m.logs.noteOn(rowKey(rowServer, name), note)
	return m.work.do(func(ctx context.Context) tea.Msg {
		if connect {
			return reconnectedMsg{name, b.ConnectServer(ctx, name)}
		}
		return reconnectedMsg{name, b.DisconnectServer(ctx, name)}
	})
}

// reconnect ends a server's connection and starts it again.
func (m *Model) reconnect(name string) tea.Cmd {
	m.panelErr = ""
	m.logs.noteOn(rowKey(rowServer, name), "Reconnecting "+name+".")
	b := m.b
	return m.work.do(func(ctx context.Context) tea.Msg {
		return reconnectedMsg{name, b.ReconnectServer(ctx, name)}
	})
}
