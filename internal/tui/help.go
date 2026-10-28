package tui

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// newHelp is the footer's help, in the brand's colours.
func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = footerSep
	h.Styles.ShortKey = styleBeacon
	h.Styles.ShortDesc = styleDim
	h.Styles.ShortSeparator = styleDim
	h.Styles.Ellipsis = styleDim
	return h
}

// bindings are footer items as key bindings, for the help to draw.
func bindings(items []footerItem) []key.Binding {
	out := make([]key.Binding, len(items))
	for i, it := range items {
		out[i] = key.NewBinding(key.WithKeys(it.key), key.WithHelp(it.key, it.label))
	}
	return out
}

// helpGroup is a column of the full help.
type helpGroup struct {
	title string
	keys  []key.Binding
}

// helpGroups are every key of the main screen, in four columns.
var helpGroups = []helpGroup{
	{"move · mouse", bindings([]footerItem{{"j/k", "up and down"}, {"h", "home"}, {"tab", "routes"}, {"/", "filter by name"}, {"esc", "clear filter"},
		{"drag", "copy text"}, {"2click", "copy a word"}, {"click", "open an address"}})},
	{"project", bindings([]footerItem{{"enter", "start or stop"}, {"o", "open in browser"}, {"e", "edit"}, {"d", "remove"}, {"y", "copy advice"},
		{"i", "show or hide ignored ports"},
		{"r", "re-read .nearhostrc"}, {"a", "review .nearhostrc"}})},
	{"server", bindings([]footerItem{{"s", "add server"}, {"r", "reconnect"}, {"c", "connect or drop"}, {"e", "edit"}, {"d", "remove"},
		{"tab", "passthroughs, documents"}, {"enter", "open a document"}, {"x", "close, revoke or remove"}})},
	{"nearhost", bindings([]footerItem{{"n", "add project"}, {"p", "review request"}, {"l", "full-screen log"}, {"?", "close help"}, {"q", "quit"}})},
}

// helpKeyWidth is the width of the keys' column in the full help.
const helpKeyWidth = 7

// helpGap is the cells of space between the full help's columns.
const helpGap = 1

// groupWidth is the width of a group's cells: the keys' column and the
// longest description.
func groupWidth(g helpGroup) int {
	w := lipgloss.Width(g.title)
	for _, b := range g.keys {
		w = max(w, helpKeyWidth+lipgloss.Width(b.Help().Desc))
	}
	return w
}

// helpColumns is the full help's layout for a window of width: its columns,
// each a list of groups stacked top to bottom. The four groups sit side by
// side when they fit whole, and in two columns of two otherwise.
func helpColumns(width int) [][]helpGroup {
	wide := 1 // the cell at the left
	for _, g := range helpGroups {
		wide += groupWidth(g) + helpGap
	}
	if wide-helpGap <= width {
		cols := make([][]helpGroup, len(helpGroups))
		for i, g := range helpGroups {
			cols[i] = []helpGroup{g}
		}
		return cols
	}
	half := len(helpGroups) / 2
	return [][]helpGroup{helpGroups[:half], helpGroups[half:]}
}

// helpRows is how many rows the full help takes in a window of width: the
// tallest column, each group with its title.
func helpRows(width int) int {
	rows := 0
	for _, col := range helpColumns(width) {
		n := 0
		for _, g := range col {
			n += len(g.keys) + 1
		}
		rows = max(rows, n)
	}
	return rows
}

// fullHelp draws every key in columns, each as wide as its longest line.
func (m *Model) fullHelp() string {
	rows := helpRows(m.width)
	var cols []string
	for _, col := range helpColumns(m.width) {
		w := 0
		for _, g := range col {
			w = max(w, groupWidth(g))
		}
		var lines []string
		for _, g := range col {
			lines = append(lines, fit(styleTitle.Render(g.title), w))
			for _, b := range g.keys {
				h := b.Help()
				lines = append(lines, fit(styleBeacon.Render(fit(h.Key, helpKeyWidth))+styleDim.Render(h.Desc), w))
			}
		}
		for len(lines) < rows {
			lines = append(lines, strings.Repeat(" ", w))
		}
		cols = append(cols, strings.Join(lines, "\n"), strings.Repeat(" ", helpGap))
	}
	block := lipgloss.JoinHorizontal(lipgloss.Top, cols[:len(cols)-1]...)
	out := strings.Split(block, "\n")
	for i, l := range out {
		out[i] = " " + fit(l, m.width-1)
	}
	return strings.Join(out, "\n")
}
