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

// emptyWidth is the width of the first-run screen's text.
const emptyWidth = 56

// empty reports whether the first-run screen is drawn: there is nothing to
// serve and no form is open.
func (m *Model) empty() bool {
	return len(m.cfg.Projects) == 0 && m.form == nil
}

// emptyKeys are the keys that work on the first-run screen.
var emptyKeys = []footerItem{{"n", "add project"}, {"s", "add server"}, {"?", "help"}, {"q", "quit"}}

// emptyScreen is the first-run screen: the mark and the name, what to do,
// and how the machine is, in the middle of the window over the footer.
func (m *Model) emptyScreen() string {
	centre := func(s string) string { return lipgloss.PlaceHorizontal(emptyWidth, lipgloss.Center, s) }
	var lines []string
	for _, l := range markLines {
		lines = append(lines, centre(l))
	}
	lines = append(lines, "", centre(styleTitle.Render("nearhost")), "", "",
		centre(styleTitle.Render("Nothing to serve yet.")),
		centre("Press "+styleBeacon.Render("n")+" to add the project in the current directory."), "")
	ca := styleBeacon.Render("●") + " trusted"
	if !m.caTrusted {
		ca = styleAmber.Render("● not trusted") + ". Run nearhost trust"
	}
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
