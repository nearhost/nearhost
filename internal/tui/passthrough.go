package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// requesterName is how a requester is named in a sentence. The tools Nearhost
// knows get their proper names; anything else is shown as it was given, only
// cleaned, never prettified.
func requesterName(r string) string {
	switch r {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "agent":
		return "An agent"
	}
	return clean(r)
}

// where names the request's server in a sentence: "on apollo".
func (r PassRequest) where() string { return "on " + clean(r.Origin) }

// who names the requester and its server: "Claude Code on apollo".
func (r PassRequest) who() string { return requesterName(r.Requester) + " " + r.where() }

// passForm asks whether to open a port for an agent: yes, no, or always
// allow. The answer is given on the worker, since opening the forward blocks.
type passForm struct {
	b    Backend
	w    *worker
	r    PassRequest
	busy bool
	err  string
	seq  int
}

// passAnsweredMsg is the outcome of answering a request.
type passAnsweredMsg struct {
	seq    int
	r      PassRequest
	answer Answer
	err    error
}

func (f *passForm) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if f.busy {
			return nil // an answer runs to its end
		}
		switch msg.String() {
		case "y":
			return f.answer(AnswerYes)
		case "a":
			if f.r.CanAlways {
				return f.answer(AnswerAlways)
			}
		case "n", "esc":
			return f.answer(AnswerNo)
		}
	}
	return nil
}

// answer gives a to the backend on the worker.
func (f *passForm) answer(a Answer) tea.Cmd {
	f.seq = nextSeq()
	f.busy, f.err = true, ""
	seq, b, r := f.seq, f.b, f.r
	return f.w.do(func(ctx context.Context) tea.Msg {
		return passAnsweredMsg{seq, r, a, b.AnswerPassthrough(ctx, r.ID, a)}
	})
}

// answered takes the outcome of an answer; it reports whether the box is done.
func (f *passForm) answered(msg passAnsweredMsg) bool {
	if msg.seq != f.seq {
		return false
	}
	f.busy = false
	if msg.err != nil {
		f.err = msg.err.Error()
		return false
	}
	return true
}

func (f *passForm) lines(width int) []string {
	what := fmt.Sprintf("wants to open port %d on this laptop", f.r.Remote)
	if l := clean(f.r.Label); l != "" {
		what += " (" + l + ")"
	}
	lines := wrap(f.r.who()+" "+what+".", width)
	if f.r.LocalTaken {
		lines = append(lines, wrap(fmt.Sprintf("Port %d is in use here, so it will open on another port.", f.r.Remote), width)...)
	}
	if f.busy {
		lines = append(lines, "", busyLine("Opening…"))
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	return lines
}

func (f *passForm) heading() heading { return heading{title: "Open a port"} }

func (f *passForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	items := []footerItem{{"y", "yes"}, {"n", "no"}}
	if f.r.CanAlways {
		items = append(items, footerItem{"a", "always allow"})
	}
	return items
}

// gotPass queues a passthrough request and says it came.
func (m *Model) gotPass(r PassRequest) tea.Cmd {
	text := fmt.Sprintf("%s wants to open port %d", r.who(), r.Remote)
	m.logs.note(text + ".")
	m.waiting = append(m.waiting, asked{pass: &r})
	return m.notify(text)
}

// passOpened tells that a passthrough opened without asking.
func (m *Model) passOpened(msg passOpenedMsg) tea.Cmd {
	text := fmt.Sprintf("%s on %s opened port %d", requesterName(msg.requester), clean(msg.origin), msg.remote)
	m.logs.note(text + ".")
	return m.showToast(text, false)
}

// passAnswered logs the outcome of an answer, whatever is open now, since the
// forward may have opened or the agent been told. It closes the box only when
// that is the one that answered.
func (m *Model) passAnswered(msg passAnsweredMsg) tea.Cmd {
	if pf, ok := m.form.(*passForm); ok && pf.answered(msg) {
		m.form = nil
	}
	if msg.err != nil {
		return nil
	}
	line := fmt.Sprintf("Opened port %d for %s.", msg.r.Remote, msg.r.who())
	if msg.answer == AnswerNo {
		line = fmt.Sprintf("Declined to open port %d for %s.", msg.r.Remote, msg.r.who())
	}
	m.logs.note(line)
	return nil
}

// expires is when the agent stops waiting for an answer.
func (a asked) expires() time.Time {
	if a.pass != nil {
		return a.pass.Expires
	}
	return a.prop.Expires
}
