package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/nearhost/nearhost/internal/config"
)

// The quit box's words. The buttons are drawn in brackets, so a click can
// find them on the screen.
const (
	quitQuestion = "Stop Nearhost, or leave it running in the background?"
	stopButton   = "[ Stop ]"
	leaveButton  = "[ Leave running ]"
	rememberText = "Don't ask again"
)

// quitBox asks whether quitting stops Nearhost or leaves it running in the
// background, and offers to remember the answer. The model hands it keys and
// clicks, and does what it answers.
type quitBox struct {
	leave    bool // Leave running has the focus; Stop has it otherwise
	remember bool // Don't ask again is ticked
}

// quitAnswer is what a key or click at the quit box comes to.
type quitAnswer int

const (
	quitNothing quitAnswer = iota // the box stays open
	quitClose                     // the box closes and the interface stays
	quitChosen                    // the focused button was chosen
)

// key handles a key at the box: tab and the arrows move between the
// buttons, space ticks the check box, enter chooses the focused button and
// esc closes the box.
func (b *quitBox) key(s string) quitAnswer {
	switch s {
	case "tab", "shift+tab", "left", "right":
		b.leave = !b.leave
	case "space", " ":
		b.remember = !b.remember
	case "enter":
		return quitChosen
	case "esc":
		return quitClose
	}
	return quitNothing
}

// click handles a click at p on the screen's lines: on a button it chooses
// it, and on the check box it ticks it.
func (b *quitBox) click(lines []uv.Line, p point) quitAnswer {
	if p.y < 0 || p.y >= len(lines) {
		return quitNothing
	}
	row := lines[p.y]
	on := func(s string) bool {
		lo, hi, ok := textSpan(row, s)
		return ok && p.x >= lo && p.x < hi
	}
	switch {
	case on(stopButton):
		b.leave = false
		return quitChosen
	case on(leaveButton):
		b.leave = true
		return quitChosen
	case on("[ ] " + rememberText), on("[x] " + rememberText):
		b.remember = !b.remember
	}
	return quitNothing
}

// choice is the focused button's choice.
func (b *quitBox) choice() QuitChoice {
	if b.leave {
		return QuitLeave
	}
	return QuitStop
}

func (b *quitBox) lines(width int) []string {
	button := func(label string, focused bool) string {
		if focused {
			return styleBeacon.Render("▸ ") + styleSelected.Render(label)
		}
		return "  " + label
	}
	box := "[ ]"
	if b.remember {
		box = styleBeacon.Render("[x]")
	}
	lines := wrap(quitQuestion, width)
	return append(lines, "",
		button(stopButton, !b.leave)+"   "+button(leaveButton, b.leave),
		"",
		"  "+box+" "+rememberText)
}

func (b *quitBox) keys() []footerItem {
	return []footerItem{{"enter", "choose"}, {"tab", "move"}, {"space", "tick"}, {"esc", "cancel"}}
}

func (b *quitBox) heading() heading { return heading{title: "Quit Nearhost"} }

// textSpan is the cells [lo, hi) the first s in row takes; ok is false when
// row does not hold s.
func textSpan(row uv.Line, s string) (lo, hi int, ok bool) {
	var sb strings.Builder
	col := make([]int, 0, len(row)) // the cell of each byte
	for x, c := range row {
		sb.WriteString(c.Content)
		for range len(c.Content) {
			col = append(col, x)
		}
	}
	at := strings.Index(sb.String(), s)
	if at < 0 || s == "" {
		return 0, 0, false
	}
	return col[at], col[at+len(s)-1] + 1, true
}

// quit quits as the backend's QuitChoice says: it stops Nearhost, leaves it
// running, or asks which in the quit box. Asked again while quitting, it
// cuts the quit short. ctrlC is set for Ctrl-C, which is also how a hangup
// or terminate signal arrives: it never asks, as nobody may be there to
// answer, and at the quit box it stops, as q does in a window too small to
// draw the box.
func (m *Model) quit(ctrlC bool) tea.Cmd {
	if m.quitting {
		if m.cutShort != nil { // nil once the core is lost: nothing runs
			m.cutShort()
		}
		return nil
	}
	if m.quitBox != nil {
		if ctrlC || m.small() {
			return m.end(QuitStop, false)
		}
		return nil
	}
	switch m.b.QuitChoice() {
	case QuitLeave:
		return m.end(QuitLeave, false)
	case QuitAsk:
		// A window too small for the box quits as the box would by
		// default: it stops.
		if !ctrlC && !m.small() {
			m.quitBox = &quitBox{}
			return nil
		}
	}
	return m.end(QuitStop, false)
}

// quitBoxKey handles a key while the quit box is open.
func (m *Model) quitBoxKey(s string) tea.Cmd {
	return m.quitAnswered(m.quitBox.key(s))
}

// quitAnswered does what the quit box answered.
func (m *Model) quitAnswered(a quitAnswer) tea.Cmd {
	switch a {
	case quitClose:
		m.quitBox = nil
	case quitChosen:
		return m.end(m.quitBox.choice(), m.quitBox.remember)
	}
	return nil
}

// end ends the interface: it stops Nearhost, or leaves it running, then
// ends the program. With remember, the choice is saved as keep_running
// first, so quitting does not ask again; a failure to save it is noted and
// the quit goes ahead.
func (m *Model) end(how QuitChoice, remember bool) tea.Cmd {
	m.quitBox = nil
	m.quitting, m.leaving = true, how == QuitLeave
	if m.password != nil {
		m.password.answer(ErrPasswordCancelled)
		m.password = nil
	}
	if m.endApply != nil {
		m.endApply() // the shutdown would otherwise wait for it
	}
	// The open form's connection, and those its running steps are opening,
	// go before the engine does. The connection closes first, as a step may
	// be stuck reading from it, then the steps are waited for, but only for
	// formStopTimeout, so the engine keeps the rest of the time to stop.
	var unblock func()
	if pf, ok := m.form.(*projectForm); ok && pf.files != nil {
		files := pf.files
		pf.files = nil
		unblock = func() { files.Close() }
	}
	work, b := m.work, m.b
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	m.cutShort = cancel
	return func() tea.Msg {
		defer cancel()
		var unsaved error
		if remember {
			keep := how == QuitLeave
			unsaved = b.EditConfig(func(cfg *config.Config) error {
				cfg.KeepRunning = &keep
				return nil
			})
		}
		steps, stopped := context.WithTimeout(ctx, formStopTimeout)
		work.stop(steps, unblock)
		stopped()
		if how == QuitLeave {
			return shutdownMsg{err: b.Leave(ctx), unsaved: unsaved}
		}
		return shutdownMsg{err: b.Shutdown(ctx), unsaved: unsaved}
	}
}

// lostMsg says the core can no longer be reached.
type lostMsg struct{ err error }

// waitLost waits for the backend to say the core is lost; nil when it never
// can.
func (m *Model) waitLost() tea.Cmd {
	lost := m.b.Lost()
	if lost == nil {
		return nil
	}
	return func() tea.Msg {
		err, ok := <-lost
		if !ok {
			return nil
		}
		return lostMsg{err}
	}
}

// lose ends the program because the core is lost, with nothing to stop or
// leave. While quitting the loss is the quit's own doing, and the quit ends
// the program.
func (m *Model) lose(err error) tea.Cmd {
	if m.quitting {
		return nil
	}
	m.quitting, m.lostErr, m.quitBox = true, err, nil
	if m.password != nil {
		m.password.answer(ErrPasswordCancelled)
		m.password = nil
	}
	if m.endApply != nil {
		m.endApply()
	}
	if pf, ok := m.form.(*projectForm); ok && pf.files != nil {
		pf.files.Close()
		pf.files = nil
	}
	m.unsubscribe()
	return tea.Quit
}

// Err is why the program ended other than by quitting: the error that
// said the core was lost. It is nil after a quit.
func (m *Model) Err() error { return m.lostErr }
