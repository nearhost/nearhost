package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// markLines draw the Nearhost mark in half blocks: an arch in the beacon
// colour over a dot, 9 cells by 4.
var markLines = []string{
	styleBeacon.Render(" ▄█████▄ "),
	styleBeacon.Render("██     ██"),
	styleBeacon.Render("██") + styleText.Render(" ▄▄▄ ") + styleBeacon.Render("██"),
	styleBeacon.Render("██") + styleText.Render(" ▀▀▀ ") + styleBeacon.Render("██"),
}

// shortMarkLines draw the mark in 2 rows for cells about twice as tall as
// usual, at half the height in pixels: the lintel, legs and dot keep their
// shape where markLines would stand twice as tall as it is wide.
var shortMarkLines = []string{
	styleBeacon.Render("▄█▀▀▀▀▀█▄"),
	styleBeacon.Render("██") + styleText.Render(" ▀▀▀ ") + styleBeacon.Render("██"),
}

// cellsTall reports whether cells of w by h pixels are tall enough that
// shortMarkLines comes nearer the mark's shape than markLines. markLines is
// square in cells twice as tall as wide; the short mark has half the rows,
// so it is the nearer from 8/3 up, where the two are equally far off.
func cellsTall(w, h int) bool {
	return w > 0 && 3*h > 8*w
}

// emptyWidth is the width of the first-run screen's text.
const emptyWidth = 56

// empty reports whether the first-run screen is drawn: there is nothing to
// serve. A form opens over it.
func (m *Model) empty() bool {
	return len(m.cfg.Projects) == 0
}

// emptyKeys are the keys that work on the first-run screen.
var emptyKeys = []footerItem{{"n", "add project"}, {"s", "add server"}, {"?", "help"}, {"q", "quit"}}

// emptyAdding says what the two ways to start do.
var emptyAdding = []string{
	"n  add a project on this machine or a server",
	"s  add a server you reach over SSH",
}

// emptyScreen is the first-run screen: the mark and the name, what to do,
// and how the machine is, in the middle of the window over the footer.
func (m *Model) emptyScreen() string {
	centre := func(s string) string { return lipgloss.PlaceHorizontal(emptyWidth, lipgloss.Center, s) }
	var lines []string
	mark := markLines
	if m.tallCells {
		mark = shortMarkLines
	}
	for _, l := range mark {
		lines = append(lines, centre(l))
	}
	lines = append(lines, "", centre(styleTitle.Render("nearhost")), "",
		centre(styleTitle.Render("Nothing to serve yet.")), "")
	// The two keys line up with the fields below.
	for _, l := range emptyAdding {
		lines = append(lines, styleBeacon.Render(l[:1])+l[1:])
	}
	lines = append(lines, "")
	ca := m.caText()
	lines = append(lines, field("directory", clean(m.dir), emptyWidth)...)
	lines = append(lines, field("local CA", ca, emptyWidth)...)
	lines = append(lines, m.proxyLines(emptyWidth)...)
	lines = append(lines, m.problemLines(emptyWidth)...)
	for i, l := range lines {
		lines[i] = fit(l, emptyWidth)
	}
	body := lipgloss.Place(m.width, m.height-1-m.footerHeight(), lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
	return body + "\n" + rule(m.width) + "\n" + m.footer()
}
