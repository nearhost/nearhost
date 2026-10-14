package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// footerItem is one action shown in the footer: the key and what it does.
type footerItem struct{ key, label string }

// footerItems are the actions that work in the current context.
func (m *Model) footerItems() []footerItem {
	if m.quitting {
		return nil
	}
	if m.form != nil {
		return m.form.keys()
	}
	if m.routeFocus {
		return []footerItem{{"↑↓", "route"}, {"space", "loopback/ignore"}, {"w", "web"}, {"y", "copy advice"}, {"tab", "done"}}
	}
	if m.filtering {
		return []footerItem{{"enter", "keep filter"}, {"esc", "clear filter"}}
	}
	kind, _ := m.selected()
	var items []footerItem
	switch kind {
	case rowProject:
		items = []footerItem{{"enter", "start/stop"}, {"o", "open"}, {"tab", "routes"}, {"e", "edit"}, {"d", "remove"}}
	case rowServer:
		items = []footerItem{{"enter", "collapse"}}
	}
	return append(items, footerItem{"/", "filter"}, footerItem{"s", "server"}, footerItem{"n", "add"}, footerItem{"q", "quit"})
}

const footerSep = " · "

// footer draws the footer line.
func (m *Model) footer() string {
	if m.quitting {
		return styleAmber.Render(fit("Stopping…", m.width))
	}
	parts := make([]string, 0, len(m.footerItems()))
	for _, it := range m.footerItems() {
		parts = append(parts, styleBeacon.Render(it.key)+" "+styleDim.Render(it.label))
	}
	return fit(strings.Join(parts, styleDim.Render(footerSep)), m.width)
}

// footerKeyAt is the key of the footer action at column x, or "".
func (m *Model) footerKeyAt(x int) string {
	at := 0
	for i, it := range m.footerItems() {
		if i > 0 {
			at += ansi.StringWidth(footerSep)
		}
		w := ansi.StringWidth(it.key + " " + it.label)
		if x >= at && x < at+w {
			return it.key
		}
		at += w
	}
	return ""
}

// key handles a key press.
func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		return m.quit()
	}
	if m.quitting {
		if s == "q" {
			return m.quit()
		}
		return nil
	}
	if m.form != nil {
		return m.form.update(k)
	}
	if m.routeFocus && !m.filtering {
		if cmd, ok := m.routesKey(s); ok {
			return cmd
		}
	}
	if m.filtering {
		switch s {
		case "enter":
			m.filtering = false
		case "esc":
			m.filtering, m.filter = false, ""
		case "backspace":
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		default:
			if k.Text != "" {
				m.filter += clean(k.Text)
			}
		}
		m.scroll = 0
		return nil
	}
	return m.action(s)
}

// action runs what the key or footer action s does.
func (m *Model) action(s string) tea.Cmd {
	switch s {
	case "e":
		if kind, name := m.selected(); kind == rowProject {
			if p, ok := m.cfg.Project(name); ok {
				f, connect := newProjectEditForm(m.b, m.cfg, *p, m.work)
				m.form = f
				return connect
			}
		}
	case "d":
		if kind, name := m.selected(); kind == rowProject {
			if p, ok := m.cfg.Project(name); ok {
				m.removeProject(*p)
			}
		}
	case "tab":
		if kind, _ := m.selected(); kind == rowProject {
			m.routeFocus, m.routeRow, m.panelErr = true, 0, ""
		}
	case "y":
		if kind, name := m.selected(); kind == rowProject {
			return m.copyAdvice(name)
		}
	case "q":
		return m.quit()
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "h", "esc":
		if m.filter != "" && s == "esc" {
			m.filter = ""
			return nil
		}
		m.selectRow(homeKey)
	case "/":
		m.filtering = true
	case "n":
		server := config.LocalServer
		switch kind, name := m.selected(); kind {
		case rowServer:
			server = name
		case rowProject:
			if p, ok := m.cfg.Project(name); ok {
				server = p.Server
			}
		}
		m.form = newProjectForm(m.b, m.cfg, m.dir, server, m.work)
	case "s":
		f, load := newServerForm(m.b, m.cfg, m.work)
		m.form = f
		return load
	case "enter":
		return m.enter()
	case "o":
		kind, name := m.selected()
		if kind != rowProject {
			return nil
		}
		p, _ := m.cfg.Project(name)
		url := "https://" + p.Alias
		return func() tea.Msg { return openedMsg{url, m.b.Open(url)} }
	}
	return nil
}

// selectRow selects the row with key. The focus on a project's routes, and
// what the panel said about the last change made there, end with it.
func (m *Model) selectRow(key string) {
	if key != m.sel {
		m.routeFocus, m.panelErr = false, ""
	}
	m.sel, m.scroll = key, 0
}

// move moves the selection by delta rows, stopping at either end.
func (m *Model) move(delta int) {
	all := rows(m.cfg, m.collapsed)
	at := 0
	for i, r := range all {
		if r.key() == m.sel {
			at = i
		}
	}
	at = min(max(at+delta, 0), len(all)-1)
	m.selectRow(all[at].key())
}

// enter starts or stops the selected project, or collapses or expands the
// selected server.
func (m *Model) enter() tea.Cmd {
	kind, name := m.selected()
	switch kind {
	case rowServer:
		m.collapsed[name] = !m.collapsed[name]
	case rowProject:
		switch m.b.Status(name).State {
		case engine.StateStopped:
			return m.startProject(name)
		case engine.StateError:
			// A project in error may still serve some routes; enter tries
			// again, as starting it removes what its last attempt left.
			return m.startProject(name)
		default:
			return m.stopProject(name)
		}
	}
	return nil
}

// click handles a mouse click: a sidebar row selects it, a footer action
// runs it.
func (m *Model) click(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft || m.quitting || m.form != nil || m.width < minWidth || m.height < minHeight {
		return nil
	}
	if mouse.Y == m.height-1 {
		if key := m.footerKeyAt(mouse.X); key != "" {
			return m.action(key)
		}
		return nil
	}
	if mouse.X < sidebarWidth {
		all := rows(m.cfg, m.collapsed)
		if i := sidebarTop(all, m.sel, m.height-1) + mouse.Y; i < len(all) {
			m.selectRow(all[i].key())
		}
	}
	return nil
}

// wheel scrolls the log when the pointer is over the main panel.
func (m *Model) wheel(mouse tea.Mouse) {
	if mouse.X < sidebarWidth || m.form != nil {
		return
	}
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.scroll = min(m.scroll+3, len(m.logs.lines(m.sel, m.filter)))
	case tea.MouseWheelDown:
		m.scroll = max(m.scroll-3, 0)
	}
}
