package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// firstRunForm explains the two password prompts and trusts the local CA.
// It is shown once a session, when the system does not trust the CA.
type firstRunForm struct {
	b    Backend
	w    *worker
	busy bool
	err  string
	seq  int
}

type (
	// trustedMsg is the outcome of trusting the CA.
	trustedMsg struct {
		seq int
		err error
	}
	// caTrustedMsg closes the first-run screen after the CA was trusted.
	caTrustedMsg struct{}
	// firstRunSkippedMsg closes the first-run screen for the session.
	firstRunSkippedMsg struct{}
)

func (f *firstRunForm) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case trustedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = false
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		return func() tea.Msg { return caTrustedMsg{} }
	case tea.KeyPressMsg:
		if f.busy {
			return nil
		}
		switch msg.String() {
		case "enter":
			f.seq = nextSeq()
			f.busy, f.err = true, ""
			seq, b := f.seq, f.b
			return f.w.do(func(ctx context.Context) tea.Msg { return trustedMsg{seq, b.Trust(ctx)} })
		case "s":
			return func() tea.Msg { return firstRunSkippedMsg{} }
		}
	}
	return nil
}

func (f *firstRunForm) lines(width int) []string {
	lines := []string{
		"Two things ask for your password:",
		"• now: trusting the local certificate authority,",
		"  so browsers accept HTTPS from Nearhost's web server",
		"• later: adding names to /etc/hosts",
	}
	if f.busy {
		lines = append(lines, "", busyLine("Trusting the local certificate authority…"))
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	return lines
}

func (f *firstRunForm) heading() heading { return heading{title: "Welcome to Nearhost"} }

func (f *firstRunForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	label := "trust now"
	if f.err != "" {
		label = "try again"
	}
	return []footerItem{{"enter", label}, {"s", "skip"}}
}

// offerFirstRun opens the first-run screen at the first trust check after
// the engine started, when the CA is untrusted. It is offered then or not at
// all: a form open at that moment is not interrupted, and the screen does
// not open later over what the user is doing; Home's hint says what to do.
func (m *Model) offerFirstRun() {
	if m.firstRunShown || !m.started {
		return
	}
	m.firstRunShown = true
	if m.caTrusted || m.form != nil || m.quitting {
		return
	}
	m.form = &firstRunForm{b: m.b, w: m.work}
}
