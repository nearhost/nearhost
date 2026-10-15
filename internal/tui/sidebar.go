package tui

import (
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

type rowKind int

const (
	rowHome rowKind = iota
	rowServer
	rowProject
)

// row is one line of the sidebar.
type row struct {
	kind rowKind
	name string // server or project name; empty for Home
}

const homeKey = "home"

// rowKey identifies a row across config changes.
func rowKey(kind rowKind, name string) string {
	switch kind {
	case rowServer:
		return "server:" + name
	case rowProject:
		return "project:" + name
	}
	return homeKey
}

func (r row) key() string { return rowKey(r.kind, r.name) }

// rows lists the sidebar: Home, then Local and each server in config order,
// each followed by its projects in config order unless it is collapsed.
func rows(cfg *config.Config, collapsed map[string]bool) []row {
	out := []row{{kind: rowHome}}
	servers := []string{config.LocalServer}
	for _, s := range cfg.Servers {
		servers = append(servers, s.Name)
	}
	for _, s := range servers {
		out = append(out, row{kind: rowServer, name: s})
		if collapsed[s] {
			continue
		}
		for _, p := range cfg.Projects {
			if p.Server == s {
				out = append(out, row{kind: rowProject, name: p.Name})
			}
		}
	}
	return out
}

// sidebar draws the sidebar, height lines tall and width wide (the border
// is drawn by the caller).
func (m *Model) sidebar(width, height int) []string {
	all := rows(m.cfg, m.collapsed)
	lines := make([]string, 0, height)
	for _, r := range all[sidebarTop(all, m.sel, height):] {
		if len(lines) == height {
			break
		}
		text := m.rowText(r, width)
		if r.key() == m.sel {
			text = styleSelected.Render(fit(clean(text), width))
		}
		lines = append(lines, text)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

// sidebarTop is the first row drawn in a sidebar height rows tall: the
// first, unless the selected row would then be below the last line.
func sidebarTop(all []row, sel string, height int) int {
	for i, r := range all {
		if r.key() == sel {
			return max(0, i-height+1)
		}
	}
	return 0
}

// rowText is one sidebar row, exactly width cells wide.
func (m *Model) rowText(r row, width int) string {
	switch r.kind {
	case rowHome:
		return fit(styleTitle.Render("Nearhost"), width)
	case rowServer:
		arrow := "▾"
		if m.collapsed[r.name] {
			arrow = "▸"
		}
		title := "Local"
		dot, note := "", ""
		if r.name != config.LocalServer {
			title = clean(r.name)
			st, _ := m.b.ServerStatus(r.name)
			dot = " " + serverDot(st)
			if st != transport.StatusConnected && st != transport.StatusDisconnected {
				note = " " + styleDim.Render(string(st))
			}
		}
		return fit(arrow+" "+title+dot+note, width)
	default:
		p, _ := m.cfg.Project(r.name)
		st := m.b.Status(r.name).State
		name := fit(clean(r.name), width-15)
		if p != nil && !p.Enabled {
			name = styleDim.Render(name)
		}
		return fit("  "+stateDot(st)+" "+name+" "+styleDim.Render(string(st)), width)
	}
}
