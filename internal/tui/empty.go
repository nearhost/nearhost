package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
// serve, and no server was connected from that screen. A form opens over it.
func (m *Model) empty() bool {
	return len(m.cfg.Projects) == 0 && !m.leftEmpty
}

// emptyKeys are the keys that work on the first-run screen: c only once
// there is a server to connect.
func (m *Model) emptyKeys() []footerItem {
	keys := []footerItem{{"n", "add project"}, {"s", "add server"}}
	if len(m.cfg.Servers) > 0 {
		keys = append(keys, footerItem{"c", "connect server"})
	}
	return append(keys, footerItem{"?", "help"}, footerItem{"q", "quit"})
}

// emptyKey reports whether s works on the first-run screen.
func (m *Model) emptyKey(s string) bool {
	for _, it := range m.emptyKeys() {
		if it.key == s {
			return true
		}
	}
	return s == "esc"
}

// emptyAdding says what the ways to start do: c only once there is a
// server to connect.
func (m *Model) emptyAdding() []string {
	lines := []string{
		"n  add a project on this machine or a server",
		"s  add a server you reach over SSH",
	}
	if len(m.cfg.Servers) > 0 {
		lines = append(lines, "c  connect a server, so its agents reach Nearhost")
	}
	return lines
}

// connectFirst is c on the first-run screen: it connects the only server,
// or asks which one when there are several.
func (m *Model) connectFirst() tea.Cmd {
	switch len(m.cfg.Servers) {
	case 0:
		return nil
	case 1:
		return m.connectFromEmpty(m.cfg.Servers[0].Name)
	}
	var names []string
	for _, s := range m.cfg.Servers {
		names = append(names, s.Name)
	}
	m.form = &connectForm{names: names}
	return nil
}

// connectFromEmpty connects the server called name and leaves the
// first-run screen for the main screen, on that server, for the rest of
// this run. Nothing is saved: at its next start Nearhost connects the
// servers that have projects, as always.
func (m *Model) connectFromEmpty(name string) tea.Cmd {
	m.leftEmpty = true
	m.selectRow(rowKey(rowServer, name))
	return m.connect(name)
}

// connectForm asks which server the first-run screen connects.
type connectForm struct {
	names []string
	at    int
}

// serverChosenMsg is the server the connect form chose.
type serverChosenMsg struct{ name string }

func (f *connectForm) update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "up", "k":
		f.at = max(f.at-1, 0)
	case "down", "j":
		f.at = min(f.at+1, len(f.names)-1)
	case "enter":
		name := f.names[f.at]
		return func() tea.Msg { return serverChosenMsg{name} }
	case "esc":
		return closeForm
	}
	return nil
}

func (f *connectForm) lines(width int) []string {
	lines := wrap("Nearhost connects it for this run, so that an agent there can ask to add its project.", width)
	lines = append(lines, "")
	for i, n := range f.names {
		if i == f.at {
			lines = append(lines, styleBeacon.Render("▸ ")+clean(n))
		} else {
			lines = append(lines, "  "+clean(n))
		}
	}
	return lines
}

func (f *connectForm) keys() []footerItem {
	return []footerItem{{"↑↓", "server"}, {"enter", "connect"}, {"esc", "cancel"}}
}

func (f *connectForm) heading() heading { return heading{title: "Connect a server"} }

// emptyScreen is the first-run screen: the mark and the name, what to do,
// and how the machine is, in the middle of the window over the footer.
func (m *Model) emptyScreen() string {
	centre := func(s string) string { return lipgloss.PlaceHorizontal(emptyWidth, lipgloss.Center, s) }
	var lines []string
	mark := markLines
	if m.tallCells {
		mark = shortMarkLines
	}
	markRows := len(mark)
	for _, l := range mark {
		lines = append(lines, centre(l))
	}
	lines = append(lines, "", centre(styleTitle.Render("nearhost")), "",
		centre(styleTitle.Render("Nothing to serve yet.")), "")
	// The two keys line up with the fields below.
	for _, l := range m.emptyAdding() {
		lines = append(lines, styleBeacon.Render(l[:1])+l[1:])
	}
	lines = append(lines, "")
	ca := m.caText()
	lines = append(lines, field("directory", clean(m.dir), emptyWidth)...)
	lines = append(lines, field("local CA", ca, emptyWidth)...)
	lines = append(lines, m.proxyLines(emptyWidth)...)
	lines = append(lines, m.webLines(emptyWidth)...)
	lines = append(lines, m.problemLines(emptyWidth)...)
	for i, l := range lines {
		lines[i] = fit(l, emptyWidth)
	}
	room := max(m.height-1-m.footerHeight(), 0)
	body := lipgloss.Place(m.width, room, lipgloss.Center, lipgloss.Center, strings.Join(emptyFit(lines, markRows, room), "\n"))
	return body + "\n" + rule(m.width) + "\n" + m.footer()
}

// emptyFit cuts lines, whose first markRows are the mark, to room rows. The
// mark goes first, then blank lines, then lines from the bottom.
func emptyFit(lines []string, markRows, room int) []string {
	if len(lines) <= room {
		return lines
	}
	lines = lines[min(markRows, len(lines)):]
	if len(lines) <= room {
		return lines
	}
	kept := lines[:0:0]
	for _, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > room {
		kept = kept[:room]
	}
	return kept
}
