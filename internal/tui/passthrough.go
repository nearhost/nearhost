package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
// Once an answer fails, the box shows why and can only be closed: n or esc
// close it, and decline the request on the worker when it may still be
// waiting.
type passForm struct {
	b    Backend
	w    *worker
	r    PassRequest
	busy bool
	err  string
	// ended is set when the failed answer found the request no longer
	// waiting, or ended it.
	ended bool
	// opened is set when the failed answer still answered the request:
	// the port opened, and only Always allow was not saved.
	opened bool
	seq    int
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
		if f.err != "" {
			// The answer failed: another would fail the same way, or open
			// a port the user saw fail, so the box can only be closed.
			switch msg.String() {
			case "n", "esc":
				if f.ended || f.opened {
					return closeForm
				}
				return tea.Batch(closeForm, f.decline())
			}
			return nil
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

// decline says no to the request on the worker, as the box closes after a
// failed answer that left the request waiting. It is best effort: the
// outcome is not shown or logged, since the box has gone.
func (f *passForm) decline() tea.Cmd {
	b, id := f.b, f.r.ID
	return f.w.do(func(ctx context.Context) tea.Msg {
		_ = b.AnswerPassthrough(ctx, id, AnswerNo)
		return nil
	})
}

// answered takes the outcome of an answer; it reports whether the box is done.
func (f *passForm) answered(msg passAnsweredMsg) bool {
	if msg.seq != f.seq {
		return false
	}
	f.busy = false
	if msg.err != nil {
		f.err, f.ended = msg.err.Error(), errors.Is(msg.err, ErrRequestEnded)
		f.opened = errors.Is(msg.err, ErrAllowNotSaved)
		// A No on a request that has ended has nothing to show: the
		// request is over, as the No wanted.
		return msg.answer == AnswerNo && f.ended
	}
	return true
}

// shows reports whether the box is open on the answer msg is the outcome of.
func (f *passForm) shows(msg passAnsweredMsg) bool { return msg.seq == f.seq }

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
		if f.ended {
			lines = append(lines, "", "This request has ended, so the agent has stopped waiting for it.")
		}
	}
	return lines
}

func (f *passForm) heading() heading { return heading{title: "Open a port"} }

func (f *passForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	if f.err != "" {
		return []footerItem{{"esc", "close"}}
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
// that is the one that answered. A failure the box shows is not logged; one
// that comes after the box has gone is. A port that opened is logged either
// way, as is a No on a request that had already ended, which closes the box.
func (m *Model) passAnswered(msg passAnsweredMsg) tea.Cmd {
	shown := false
	if pf, ok := m.form.(*passForm); ok && pf.shows(msg) {
		shown = true
		if pf.answered(msg) {
			m.form = nil
		}
	}
	switch {
	case errors.Is(msg.err, ErrRequestEnded) && (msg.answer == AnswerNo || errors.Is(msg.err, ErrAllowNotSaved)):
		// An Always allow that failed to save for a request that ended too
		// is ended first: its port was closed, so nothing was opened.
		m.logs.note(fmt.Sprintf("The request to open port %d for %s had already ended.", msg.r.Remote, msg.r.who()))
		return nil
	case errors.Is(msg.err, ErrAllowNotSaved):
		m.logs.note(fmt.Sprintf("Opened port %d for %s, but Always allow was not saved.", msg.r.Remote, msg.r.who()))
		return nil
	}
	if msg.err != nil {
		if !shown {
			verb := "open"
			if msg.answer == AnswerNo {
				verb = "decline"
			}
			m.logs.note(fmt.Sprintf("Could not %s port %d for %s: %s.", verb, msg.r.Remote, msg.r.who(), strings.TrimSuffix(clean(msg.err.Error()), ".")))
		}
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
	if a.doc != nil {
		return a.doc.Expires
	}
	return a.prop.Expires
}
