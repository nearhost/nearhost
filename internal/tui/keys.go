package tui

import (
	"fmt"

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
	if m.empty() {
		return emptyKeys
	}
	if m.routeFocus {
		return []footerItem{{"↑↓", "route"}, {"space", "loopback/ignore"}, {"w", "web"}, {"y", "copy advice"}, {"tab", "done"}}
	}
	if m.filtering {
		return []footerItem{{"↑↓", "move"}, {"enter", "keep filter"}, {"esc", "clear filter"}}
	}
	kind, name := m.selected()
	var items []footerItem
	switch kind {
	case rowProject:
		verb := "stop"
		if st := m.b.Status(name).State; st == engine.StateStopped || st == engine.StateError {
			verb = "start"
		}
		items = []footerItem{{"enter", verb}, {"o", "open"}}
		if m.b.Status(name).Pending != nil {
			items = append(items, footerItem{"a", "review"})
		}
		items = append(items, footerItem{"tab", "routes"}, footerItem{"e", "edit"}, footerItem{"d", "remove"})
	case rowHome:
		for _, p := range m.problems() {
			if p.server != "" {
				items = []footerItem{{"r", "retry"}}
				break
			}
		}
	case rowServer:
		items = []footerItem{{"enter", "collapse"}}
		if name != config.LocalServer {
			verb := "drop"
			if m.connectsOnC(name) {
				verb = "connect"
			}
			items = append(items, footerItem{"r", "reconnect"}, footerItem{"c", verb},
				footerItem{"e", "edit"}, footerItem{"d", "remove"})
		}
	}
	return append(items, footerItem{"l", "log"}, footerItem{"/", "filter"}, footerItem{"s", "server"},
		footerItem{"n", "add"}, footerItem{"?", "help"}, footerItem{"q", "quit"})
}

// minorKeys are the main screen's actions a narrow footer leaves out, the
// first first; ? lists them all.
var minorKeys = []footerItem{
	{"q", "quit"}, {"n", "add"}, {"s", "server"}, {"enter", "collapse"},
	{"tab", "routes"}, {"d", "remove"}, {"e", "edit"},
}

// shownFooterItems are the footer items that fit in width cells: on the
// main screen minor ones are left out, in order, until the rest fit, and ?
// then says there is more.
func (m *Model) shownFooterItems(width int) []footerItem {
	items := m.footerItems()
	if m.form != nil || m.routeFocus || m.filtering {
		return items
	}
	for _, minor := range minorKeys {
		if itemsWidth(items) <= width {
			return items
		}
		var kept []footerItem
		for _, it := range items {
			switch {
			case it == minor:
			case it.key == "?":
				kept = append(kept, footerItem{"?", "more"})
			default:
				kept = append(kept, it)
			}
		}
		items = kept
	}
	return items
}

// itemsWidth is the width the footer items take in a row.
func itemsWidth(items []footerItem) int {
	w := 0
	for i, it := range items {
		if i > 0 {
			w += ansi.StringWidth(footerSep)
		}
		w += ansi.StringWidth(it.key + " " + it.label)
	}
	return w
}

const footerSep = " · "

// footer draws the footer: the keys that work now, and where the selection
// is. With help open it is the full list of keys.
func (m *Model) footer() string {
	if m.quitting {
		return m.bar(styleAmber.Render("Stopping…"), "")
	}
	if m.showHelp {
		return m.fullHelp()
	}
	if m.form != nil {
		return m.bar("", "") // the form's box has its keys
	}
	right := styleDim.Render(m.footerContext())
	m.help.SetWidth(m.footerRoom())
	return m.bar(m.help.ShortHelpView(bindings(m.shownFooterItems(m.footerRoom()))), right)
}

// footerRoom is the width the footer's keys can take.
func (m *Model) footerRoom() int {
	return m.width - 3 - ansi.StringWidth(m.footerContext())
}

// footerContext says where the selection is: the project and its place
// among the projects, the server, or home.
func (m *Model) footerContext() string {
	if m.form != nil || m.empty() {
		return ""
	}
	kind, name := m.selected()
	switch kind {
	case rowProject:
		at := 0
		for i, p := range m.cfg.Projects {
			if p.Name == name {
				at = i + 1
			}
		}
		place := fmt.Sprintf("%d/%d", at, len(m.cfg.Projects))
		if m.narrow() {
			return place
		}
		return clean(name) + " " + place
	case rowServer:
		if name == config.LocalServer {
			return "local"
		}
		return clean(name)
	}
	return "home"
}

// footerKeyAt is the key of the footer action at column x, or "".
func (m *Model) footerKeyAt(x int) string {
	if m.showHelp {
		return ""
	}
	at := 1 // the footer starts a cell in
	for i, it := range m.shownFooterItems(m.footerRoom()) {
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
	if m.password != nil {
		return m.password.update(k)
	}
	if m.form != nil {
		return m.form.update(k)
	}
	if m.logView != nil {
		if s == "p" && len(m.proposals) > 0 {
			m.logView = nil // the proposal opens over the main screen
			return m.nextProposal()
		}
		return m.logViewKey(k)
	}
	if m.showHelp && (s == "?" || s == "esc") {
		m.showHelp = false
		return nil
	}
	if m.routeFocus && !m.filtering {
		if cmd, ok := m.routesKey(s); ok {
			return cmd
		}
	}
	if m.filtering {
		return m.filterKey(k)
	}
	if m.empty() && !emptyKey(s) {
		return nil // the rows these act on are not shown
	}
	return m.action(s)
}

// emptyKey reports whether s works on the first-run screen.
func emptyKey(s string) bool {
	for _, it := range emptyKeys {
		if it.key == s {
			return true
		}
	}
	return s == "esc"
}

// action runs what the key or footer action s does.
func (m *Model) action(s string) tea.Cmd {
	switch s {
	case "e":
		switch kind, name := m.selected(); kind {
		case rowProject:
			if p, ok := m.cfg.Project(name); ok {
				f, connect := newProjectEditForm(m.b, m.cfg, *p, m.work)
				m.form = f
				return connect
			}
		case rowServer:
			if srv, ok := m.cfg.Server(name); ok && name != config.LocalServer {
				f, load := newServerEditForm(m.b, m.cfg, srv, m.work)
				m.form = f
				return load
			}
		}
	case "d":
		switch kind, name := m.selected(); kind {
		case rowProject:
			if p, ok := m.cfg.Project(name); ok {
				m.removeProject(*p)
			}
		case rowServer:
			if name != config.LocalServer {
				m.removeServer(name)
			}
		}
	case "c":
		if kind, name := m.selected(); kind == rowServer && name != config.LocalServer {
			return m.toggleConnection(name)
		}
	case "r":
		switch kind, name := m.selected(); {
		case kind == rowProject:
			return m.recheck(name)
		case kind == rowServer && name != config.LocalServer:
			return m.reconnect(name)
		case kind == rowHome:
			return m.retryServers()
		}
	case "a":
		if kind, name := m.selected(); kind == rowProject {
			return m.reviewChange(name)
		}
	case "tab":
		if kind, _ := m.selected(); kind == rowProject {
			m.routeFocus, m.routeRow, m.panelErr = true, 0, ""
		}
	case "y":
		switch kind, name := m.selected(); kind {
		case rowProject:
			return m.copyAdvice(name)
		case rowHome:
			return m.copySSH()
		}
	case "q":
		return m.quit()
	case "?":
		m.showHelp = true
	case "l":
		m.openLog()
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "h", "esc":
		if m.query() != "" {
			m.clearFilter()
			if s == "esc" {
				return nil
			}
		}
		m.selectRow(homeKey)
	case "/":
		m.filtering = true
		return m.filter.Focus()
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
	case "p":
		return m.nextProposal()
	case "i":
		// The routes with the keys show every route, so i would change
		// nothing to be seen until they gave the keys back.
		if kind, _ := m.selected(); kind == rowProject && !m.routeFocus {
			m.showIgnored = !m.showIgnored
		}
	case "enter":
		return m.enter()
	case "o":
		kind, name := m.selected()
		if kind != rowProject {
			return nil
		}
		p, _ := m.cfg.Project(name)
		url, key := "https://"+p.Alias, m.sel
		return func() tea.Msg { return openedMsg{key, url, m.b.Open(url)} }
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
	all := m.rows()
	if len(all) == 0 {
		return
	}
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
		case engine.StateStopped, engine.StateError:
			// A project in error may still serve some routes; enter tries
			// again, as starting it removes what its last attempt left. A
			// disabled one starts only when no enabled project serves its
			// hosts.
			if p, ok := m.cfg.Project(name); ok {
				if err := m.enableClash(*p); err != nil {
					m.panelErr = err.Error()
					return nil
				}
			}
			return m.startProject(name)
		default:
			return m.stopProject(name)
		}
	}
	return nil
}

// clickable reports whether a click at mouse does something: on a footer
// key, or a sidebar row, while no box covers them.
func (m *Model) clickable(mouse tea.Mouse) bool {
	if mouse.Button != tea.MouseLeft || m.quitting || m.form != nil || m.password != nil || m.logView != nil || m.width < minWidth || m.height < minHeight {
		return false
	}
	if mouse.Y == m.height-1 {
		return m.footerKeyAt(mouse.X) != ""
	}
	y := mouse.Y - bodyTop
	return !m.empty() && mouse.X < m.sideWidth() && y < m.bodyHeight() && m.sidebarKeyAt(y) != ""
}

// click handles a mouse click where clickable says one does something: a
// sidebar row selects it, a footer action runs it.
func (m *Model) click(mouse tea.Mouse) tea.Cmd {
	if mouse.Y == m.height-1 {
		key := m.footerKeyAt(mouse.X)
		if key == "" {
			return nil
		}
		if m.filtering {
			// The filter's own keys, as typed: enter here is not start or
			// stop.
			switch key {
			case "enter":
				return m.filterKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			case "esc":
				return m.filterKey(tea.KeyPressMsg{Code: tea.KeyEsc})
			}
			return nil
		}
		if m.routeFocus {
			if cmd, ok := m.routesKey(key); ok {
				return cmd // the footer then lists the routes' keys
			}
		}
		return m.action(key)
	}
	y := mouse.Y - bodyTop
	if !m.empty() && mouse.X < m.sideWidth() && y < m.bodyHeight() {
		if key := m.sidebarKeyAt(y); key != "" {
			m.selectRow(key)
		}
	}
	return nil
}

// wheel scrolls the log when the pointer is over the main panel.
func (m *Model) wheel(mouse tea.Mouse) {
	if m.password != nil {
		return
	}
	if m.logView != nil && m.form == nil {
		m.logWheel(mouse)
		return
	}
	if mouse.X < m.mainX() || m.form != nil || m.empty() {
		return
	}
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.scroll = min(m.scroll+3, len(m.logs.entries(m.sel, "")))
	case tea.MouseWheelDown:
		m.scroll = max(m.scroll-3, 0)
	}
}
