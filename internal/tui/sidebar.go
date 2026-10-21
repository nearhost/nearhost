package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
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
// Filtered by query, it lists the projects whose names hold it, under
// their servers, and nothing else.
func rows(cfg *config.Config, collapsed map[string]bool, query string) []row {
	if query != "" {
		return filteredRows(cfg, query)
	}
	out := []row{{kind: rowHome}}
	for _, s := range serverNames(cfg) {
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

// filteredRows are the projects whose names hold query, under their
// servers.
func filteredRows(cfg *config.Config, query string) []row {
	var out []row
	for _, s := range serverNames(cfg) {
		var on []row
		for _, p := range cfg.Projects {
			if p.Server == s && matches(p.Name, query) {
				on = append(on, row{kind: rowProject, name: p.Name})
			}
		}
		if len(on) > 0 {
			out = append(append(out, row{kind: rowServer, name: s}), on...)
		}
	}
	return out
}

// serverNames are Local and then the configured servers, in config order.
func serverNames(cfg *config.Config) []string {
	servers := []string{config.LocalServer}
	for _, s := range cfg.Servers {
		servers = append(servers, s.Name)
	}
	return servers
}

// rows lists the sidebar as it is drawn now.
func (m *Model) rows() []row { return rows(m.cfg, m.collapsed, m.query()) }

// sideLine is a line of the sidebar and the row it draws; key is empty
// for the blank lines between groups.
type sideLine struct {
	key  string
	draw func(selected bool) string
}

// sideLines are every line of the sidebar, width wide: Home, on the main
// panel's first line, a blank line, then each server with its projects.
func (m *Model) sideLines(width int) []sideLine {
	blank := sideLine{draw: func(bool) string { return strings.Repeat(" ", width) }}
	var lines []sideLine
	if m.filtering || m.query() != "" {
		for _, l := range m.filterLines(width) {
			lines = append(lines, sideLine{draw: func(bool) string { return l }})
		}
	}
	for _, r := range m.rows() {
		lines = append(lines, sideLine{key: r.key(), draw: func(selected bool) string { return m.rowLine(r, width, selected) }})
		if r.kind == rowHome {
			lines = append(lines, blank)
		}
	}
	return lines
}

// sidebar draws the sidebar, height lines tall and width wide, and its
// scrollbar, for the caller to draw on the border.
func (m *Model) sidebar(width, height int) ([]string, []string) {
	all := m.sideLines(width)
	out := make([]string, 0, height)
	top := sidebarTop(all, m.sel, height)
	for _, l := range all[top:] {
		if len(out) == height {
			break
		}
		out = append(out, l.draw(l.key == m.sel))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out, scrollbar(top, len(all), height)
}

// sidebarTop is the first line drawn in a sidebar height lines tall: the
// first, unless the selected row would then be below the last line.
func sidebarTop(all []sideLine, sel string, height int) int {
	for i, l := range all {
		if l.key == sel {
			return max(0, i-height+1)
		}
	}
	return 0
}

// sidebarKeyAt is the key of the row drawn on line y of the sidebar, or ""
// for none.
func (m *Model) sidebarKeyAt(y int) string {
	all := m.sideLines(m.sideWidth())
	if i := sidebarTop(all, m.sel, m.bodyHeight()) + y; y >= 0 && i < len(all) {
		return all[i].key
	}
	return ""
}

// rowLine draws a row of the sidebar, width wide.
func (m *Model) rowLine(r row, width int, selected bool) string {
	name := styleText
	if selected {
		name = styleTitle
	}
	switch r.kind {
	case rowHome:
		running := 0
		for _, p := range m.cfg.Projects {
			if m.b.Status(p.Name).State == engine.StateRunning {
				running++
			}
		}
		right := seg{styleBeacon, fmt.Sprintf("%d running", running)}
		switch {
		case m.narrow():
			right.text = fmt.Sprint(running)
		case running == 0:
			right = seg{styleDim, "nothing running"}
		}
		return renderRow([]seg{{name, "home"}}, []seg{right}, width, selected)
	case rowServer:
		arrow := "▾ "
		if m.collapsed[r.name] {
			arrow = "▸ "
		}
		left := []seg{{styleDim, arrow}, {name, clean(r.name)}}
		if r.name == config.LocalServer {
			count, shown := 0, 0
			for _, p := range m.cfg.Projects {
				if p.Server == r.name {
					count++
					if matches(p.Name, m.query()) {
						shown++
					}
				}
			}
			text := fmt.Sprint(count)
			if m.query() != "" {
				text = fmt.Sprintf("%d of %d", shown, count)
			}
			return renderRow(left, []seg{{styleDim, text}}, width, selected)
		}
		h := m.b.ServerHealth(r.name)
		st := h.Status
		dot := m.serverDot(st)
		if m.narrow() {
			return renderRow(append(left, seg{styleText, " "}, dot), nil, width, selected)
		}
		word := seg{dot.style, " " + string(st)}
		if h.Held {
			word.text = " dropped"
		}
		if st == transport.StatusConnected {
			word.style = styleText
		}
		return renderRow(left, []seg{dot, word}, width, selected)
	}
	p, _ := m.cfg.Project(r.name)
	status := m.b.Status(r.name)
	st := status.State
	if p != nil && !p.Enabled {
		name = styleDim
	}
	dot := m.stateDot(st)
	if status.Pending != nil {
		dot.style = styleAmber // its .nearhostrc waits for approval
	}
	left := append([]seg{{styleText, "  "}, dot, {styleText, " "}}, highlight(clean(r.name), m.query(), seg{style: name})...)
	if m.narrow() {
		return renderRow(left, nil, width, selected)
	}
	word := seg{dot.style, string(st)}
	switch {
	case status.Pending != nil: // the word stays amber with the dot
	case selected && st == engine.StateRunning:
		word.style = styleBeacon
	case st == engine.StateRunning, st == engine.StateStopped:
		word.style = styleDim
	}
	return renderRow(left, []seg{word}, width, selected)
}

// seg is a piece of a sidebar row in one style.
type seg struct {
	style lipgloss.Style
	text  string
}

// renderRow draws a sidebar row width wide: a bar when selected, left at
// the start, right at the end, and a cell of space after it. left is cut
// short when both do not fit. The selected row is filled to its ends.
func renderRow(left, right []seg, width int, selected bool) string {
	paint := func(sg seg) string {
		if selected {
			sg.style = sg.style.Background(colourSelected)
		}
		return sg.style.Render(sg.text)
	}
	var b strings.Builder
	if selected {
		b.WriteString(paint(seg{styleBeacon, "▌"}))
	} else {
		b.WriteString(" ")
	}
	room := width - 2
	rw := segsWidth(right)
	if rw > 0 && rw+1 < room {
		room -= rw + 1
	} else {
		right, rw = nil, 0
	}
	used := 0
	for _, sg := range left {
		sg.text = ansi.Truncate(sg.text, room-used, "…")
		b.WriteString(paint(sg))
		used += ansi.StringWidth(sg.text)
	}
	gap := room - used
	if rw > 0 {
		gap++
	}
	b.WriteString(paint(seg{styleText, strings.Repeat(" ", gap)}))
	for _, sg := range right {
		b.WriteString(paint(sg))
	}
	b.WriteString(paint(seg{styleText, " "}))
	return b.String()
}

func segsWidth(segs []seg) int {
	w := 0
	for _, sg := range segs {
		w += ansi.StringWidth(sg.text)
	}
	return w
}
