package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// toastFor is how long a toast shows.
const toastFor = 2 * time.Second

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

// alertProblem is alert for something that went wrong.
func (m *Model) alertProblem(key, line string) tea.Cmd {
	m.logs.noteOn(key, line)
	return m.showToast(line, true)
}

// showToast shows text in a toast at the bottom right for toastFor.
func (m *Model) showToast(text string, problem bool) tea.Cmd {
	m.toast.seq++
	m.toast.text, m.toast.problem = text, problem
	return toastTimer(m.toast.seq)
}

// toastTimer ends toast seq after toastFor. Tests replace it with one that
// never fires, and end toasts themselves.
var toastTimer = func(seq int) tea.Cmd {
	return tea.Tick(toastFor, func(time.Time) tea.Msg { return toastDoneMsg{seq} })
}

// toastDone ends the toast msg is for, unless another has replaced it.
func (m *Model) toastDone(msg toastDoneMsg) {
	if msg.seq == m.toast.seq {
		m.toast.text = ""
	}
}

// withToast draws the toast over content, at the right, just above the
// footer's rule, and clear of the sidebar.
func (m *Model) withToast(content string) string {
	if m.toast.text == "" || m.width < minWidth || m.height < minHeight {
		return content
	}
	dot := styleBeacon.Render("●")
	if m.toast.problem {
		dot = styleAmber.Render("●")
	}
	// Over the main panel alone, where the sidebar is drawn: the sidebar's
	// rows take clicks, the toast does not.
	room := m.width - 10
	if m.logView == nil && !m.empty() {
		room = m.mainWidth() - 7 // the box's edges, padding and dot, and the gap at the right
	}
	text := ansi.Truncate(clean(m.toast.text), min(toastWidth, room), "…")
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
