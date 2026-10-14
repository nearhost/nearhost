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
			return closeForm
		}
	}
	return nil
}

func (f *firstRunForm) lines(width int) []string {
	lines := []string{
		styleTitle.Render("Welcome to Nearhost"),
		"",
		"Two things ask for your password:",
		"• now: trusting the local certificate authority,",
		"  so browsers accept https://<name>.test",
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

// offerFirstRun opens the first-run screen when the CA is untrusted, once a
// session, and not over another form.
func (m *Model) offerFirstRun() {
	if m.firstRunShown || m.caTrusted || !m.started || m.form != nil || m.quitting {
		return
	}
	m.firstRunShown = true
	m.form = &firstRunForm{b: m.b, w: m.work}
}
