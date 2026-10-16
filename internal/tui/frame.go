package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	// narrowBelow is the width under which the compact layout is drawn.
	narrowBelow = 100
	// wideSidebar and narrowSidebar are the sidebar's widths.
	wideSidebar, narrowSidebar = 28, 18
	// bodyTop is the screen row the body starts on, below the header and
	// its rule.
	bodyTop = 2
)

// narrow reports whether the window gets the compact layout.
func (m *Model) narrow() bool { return m.width < narrowBelow }

// sideWidth is the sidebar's width, its border not included.
func (m *Model) sideWidth() int {
	if m.narrow() {
		return narrowSidebar
	}
	return wideSidebar
}

// mainX is the screen column the main panel starts on: the sidebar, its
// border and two cells of space.
func (m *Model) mainX() int { return m.sideWidth() + 3 }

// mainWidth is the main panel's width, leaving a cell at the right edge.
func (m *Model) mainWidth() int { return m.width - m.mainX() - 1 }

// bodyHeight is the number of rows between the header's rule and the
// footer's.
func (m *Model) bodyHeight() int { return m.height - bodyTop - 1 - m.footerHeight() }

// footerHeight is the number of rows the footer takes.
func (m *Model) footerHeight() int {
	if m.showHelp {
		return helpRows()
	}
	return 1
}

// bar draws a line of the header or footer: a cell of space at each end,
// left at the start and right at the end.
func (m *Model) bar(left, right string) string {
	return " " + spread(left, right, m.width-2) + " "
}

// header is the top line: the mark and name, and how the machine is.
func (m *Model) header() string {
	left := styleMark.Render("∩") + " " + styleTitle.Render("nearhost")
	return m.bar(left, m.machineSummary())
}

// machineSummary says in a few words how the proxy, the local CA and the
// hosts block are. The compact layout shows one dot for all three.
func (m *Model) machineSummary() string {
	https, _ := m.b.Ports()
	sep := styleDim.Render(" · ")
	proxy := styleBeacon.Render("●") + fmt.Sprintf(" :%d", https)
	proxyOK := true
	switch {
	case m.proxyErr != nil:
		proxy, proxyOK = styleRed.Render("● down"), false
	case !m.started:
		proxy = styleAmber.Render("◐ starting")
	}
	ca := "trusted"
	if !m.caTrusted {
		ca = styleAmber.Render("not trusted")
	}
	hostsText, hostsOK := hostsWord(m.hostsState)
	problems := len(m.problems())
	if m.narrow() {
		dot := styleBeacon.Render("●")
		switch {
		case !proxyOK || problems > 0:
			dot = styleRed.Render("●")
		case !m.started || !m.caTrusted || !hostsOK:
			dot = styleAmber.Render("●")
		}
		return dot + " " + styleDim.Render("proxy · CA · hosts")
	}
	summary := styleDim.Render("proxy") + " " + proxy + sep +
		styleDim.Render("CA") + " " + ca + sep +
		styleDim.Render("hosts") + " " + hostsText
	switch {
	case problems == 1:
		summary += sep + styleRed.Render("1 problem")
	case problems > 1:
		summary += sep + styleRed.Render(fmt.Sprintf("%d problems", problems))
	}
	return summary
}

// hostsWord is what the header says about the hosts block, from what the
// backend says about it, and whether that is fine.
func hostsWord(state string) (string, bool) {
	switch {
	case state == "":
		return styleDim.Render("…"), true
	case strings.HasPrefix(state, "not managed"):
		return styleDim.Render("off"), true
	case state == "no names configured", strings.HasSuffix(state, " name"), strings.HasSuffix(state, " names"):
		return "ok", true
	}
	return styleAmber.Render("needs update"), false
}

// frame draws the whole screen: the header, the body and the footer, each
// set off by a rule.
func (m *Model) frame(body []string, footer string) string {
	lines := make([]string, 0, m.height)
	lines = append(lines, m.header(), rule(m.width))
	lines = append(lines, body...)
	lines = append(lines, rule(m.width), footer)
	return strings.Join(lines, "\n")
}

// columns draws the body: the sidebar, its border, and the main panel.
func (m *Model) columns() []string {
	height := m.bodyHeight()
	side := m.sidebar(m.sideWidth(), height)
	main := m.main(m.mainWidth(), height)
	border := styleRule.Render("│")
	lines := make([]string, height)
	for i := range lines {
		lines[i] = side[i] + border + "  " + main[i] + " "
	}
	return lines
}

// tooSmall is the screen drawn in a window smaller than the interface needs.
func (m *Model) tooSmall() string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		fmt.Sprintf("Make the window larger (%d×%d or more).", minWidth, minHeight))
}
