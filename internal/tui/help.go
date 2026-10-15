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
	{"move", bindings([]footerItem{{"j/k", "up and down"}, {"h", "home"}, {"tab", "routes"}, {"/", "filter by name"}, {"esc", "clear filter"}})},
	{"project", bindings([]footerItem{{"enter", "start or stop"}, {"o", "open in browser"}, {"e", "edit"}, {"d", "remove"}, {"y", "copy advice"}})},
	{"server", bindings([]footerItem{{"s", "add server"}, {"r", "reconnect"}, {"c", "connect or drop"}, {"e", "edit"}, {"d", "remove"}})},
	{"nearhost", bindings([]footerItem{{"n", "add project"}, {"l", "full-screen log"}, {"?", "close help"}, {"q", "quit"}})},
}

// helpRows is how many rows the full help takes: a title and the longest
// column.
func helpRows() int {
	rows := 0
	for _, g := range helpGroups {
		rows = max(rows, len(g.keys))
	}
	return rows + 1
}

// helpKeyWidth is the width of the keys' column in the full help.
const helpKeyWidth = 7

// fullHelp draws every key in columns, as wide as the window allows.
func (m *Model) fullHelp() string {
	colWidth := (m.width - 2) / len(helpGroups)
	cols := make([]string, len(helpGroups))
	for i, g := range helpGroups {
		// A cell is left between columns.
		lines := []string{fit(styleTitle.Render(g.title), colWidth-1) + " "}
		for _, b := range g.keys {
			h := b.Help()
			lines = append(lines, fit(styleBeacon.Render(fit(h.Key, helpKeyWidth))+styleDim.Render(h.Desc), colWidth-1)+" ")
		}
		for len(lines) < helpRows() {
			lines = append(lines, strings.Repeat(" ", colWidth))
		}
		cols[i] = strings.Join(lines, "\n")
	}
	block := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	out := strings.Split(block, "\n")
	for i, l := range out {
		out[i] = " " + fit(l, m.width-1)
	}
	return strings.Join(out, "\n")
}
