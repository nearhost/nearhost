package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
)

// toastShows is how long a toast shows.
const toastShows = 2 * time.Second

// toastWidth is the most a toast's text takes, in cells. The log has the
// whole line.
const toastWidth = 60

// toastState is the toast shown, if any, and whether it is about a
// problem. seq tells a toast's own timer from the timer of one it replaced.
type toastState struct {
	text    string
	problem bool
	seq     int
}

// toastDoneMsg ends the toast with seq, if it is still the one shown.
type toastDoneMsg struct{ seq int }

// alert writes line to the log of the row key, as logs.noteOn does, and
// shows it in a toast too: the way to answer something the user just did.
// What happens by itself goes to the log alone, so toasts come only when
// asked for.
func (m *Model) alert(key, line string) tea.Cmd {
	m.logs.noteOn(key, line)
	return m.showToast(line, false)
}

// toastFor shows msg, a warning or a notice, or a problem that began on
// its own, in a toast: in the problem style for a warning or an error, plain
// for a notice. The toast has the message's headline; the log, the alert
// and the problems panel have the rest.
func (m *Model) toastFor(msg *report.Message) tea.Cmd {
	if msg == nil {
		return nil
	}
	return m.showToast(headline(msg), msg.Level >= report.Warning)
}

// warn is a warning about something the user just did on the row key, such
// as a link that would not open: it goes to that row's log, whole, and to a
// toast.
func (m *Model) warn(key string, msg *report.Message) tea.Cmd {
	m.logs.noteOn(key, messageLine(msg))
	return m.toastFor(msg)
}

// problemBegins toasts the problem ev carries, a warning, an error or a
// notice that arrived on its own, as it begins: on the first event of its
// row with its code. An event of the row that carries none and says the
// row changed, a project's new state or a server's line, ends it, so the
// next problem toasts again. Such a problem never opens the alert.
func (m *Model) problemBegins(ev engine.Event) tea.Cmd {
	key := homeKey
	switch {
	case ev.Project != "":
		key = rowKey(rowProject, ev.Project)
	case ev.Server != "":
		key = rowKey(rowServer, ev.Server)
	}
	if ev.Report == nil {
		if ev.State != "" || (ev.Project == "" && ev.Server != "") {
			delete(m.toasted, key)
		}
		return nil
	}
	if code, ok := m.toasted[key]; ok && code == ev.Report.Code {
		return nil
	}
	m.toasted[key] = ev.Report.Code
	return m.toastFor(ev.Report)
}

// showToast shows text in a toast at the bottom right for toastShows.
func (m *Model) showToast(text string, problem bool) tea.Cmd {
	m.toast.seq++
	m.toast.text, m.toast.problem = text, problem
	return toastTimer(m.toast.seq)
}

// toastTimer ends toast seq after toastShows. Tests replace it with one that
// never fires, and end toasts themselves.
var toastTimer = func(seq int) tea.Cmd {
	return tea.Tick(toastShows, func(time.Time) tea.Msg { return toastDoneMsg{seq} })
}

// toastDone ends the toast msg is for, unless another has replaced it.
func (m *Model) toastDone(msg toastDoneMsg) {
	if msg.seq == m.toast.seq {
		m.toast.text = ""
	}
}

// toastRoom is the most cells a toast's text takes: toastWidth, or less where
// the main panel is narrower. It is over the main panel alone, where the
// sidebar is not drawn: the sidebar's rows take clicks, the toast does not.
func (m *Model) toastRoom() int {
	room := m.width - 10
	if m.logView == nil && !m.empty() {
		room = m.mainWidth() - 7 // the box's edges, padding and dot, and the gap at the right
	}
	return min(toastWidth, room)
}

// withToast draws the toast over content, at the right, just above the
// footer's rule, and clear of the sidebar.
func (m *Model) withToast(content string) string {
	if m.toast.text == "" || m.small() {
		return content
	}
	dot := styleBeacon.Render("●")
	if m.toast.problem {
		dot = styleAmber.Render("●")
	}
	text := ansi.Truncate(clean(m.toast.text), m.toastRoom(), "…")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colourBoxEdge).Padding(0, 1).
		Render(dot + " " + styleText.Render(text))
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	footer := m.footerHeight()
	switch {
	case m.logView != nil:
		footer = 1 // the full-screen log's footer is a row, help or not
	case m.helpHidesBody():
		footer = 0 // the help runs from the header to the bottom; the toast sits over its foot
	}
	x, y := m.width-w-2, max(m.height-footer-1-h, bodyTop)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(content),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}
