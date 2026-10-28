package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// realHome is this machine's home folder with its symlinks resolved, as the
// folders of documents on it are given.
func realHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		return r, nil
	}
	return home, nil
}

// docWhere names a document's machine in a sentence: "on apollo", "on this
// machine".
func docWhere(origin string) string {
	if origin == config.LocalServer {
		return "on this machine"
	}
	return "on " + clean(origin)
}

// who names the requester and its machine: "Claude Code on apollo".
func (r DocRequest) who() string { return requesterName(r.Requester) + " " + docWhere(r.Origin) }

// shownRoot is the folder r would serve as the prompt shows it (see
// shortRoot).
func (m *Model) shownRoot(r DocRequest) string { return m.shortRoot(r.Origin, r.Root) }

// shortRoot is a folder on origin as Nearhost shows it: below this
// machine's home as ~ for a folder on this machine, and as it was sent for
// one on a server, whose home is not this one.
func (m *Model) shortRoot(origin, root string) string {
	if origin != config.LocalServer || m.homeDir == nil {
		return clean(root)
	}
	home, err := m.homeDir()
	if err != nil || home == "" || home == "/" {
		return clean(root)
	}
	switch {
	case root == home:
		root = "~"
	case strings.HasPrefix(root, home+"/"):
		root = "~" + root[len(home):]
	}
	return clean(root)
}

// docForm asks whether to show a document for an agent: yes, no, or always
// allow. The answer is given on the worker, since serving and opening the
// document blocks. Once an answer fails, the box shows why and can only be
// closed, as passForm's does.
type docForm struct {
	b Backend
	w *worker
	r DocRequest
	// root is the folder to serve, as the box shows it.
	root string
	busy bool
	err  string
	// ended is set when the failed answer found the request no longer
	// waiting, or ended it.
	ended bool
	// opened is set when the failed answer still answered the request: the
	// document opened, and only Always allow was not saved.
	opened bool
	seq    int
}

// docAnsweredMsg is the outcome of answering a document request.
type docAnsweredMsg struct {
	seq    int
	r      DocRequest
	answer Answer
	err    error
}

func (f *docForm) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if f.busy {
			return nil // an answer runs to its end
		}
		if f.err != "" {
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
func (f *docForm) answer(a Answer) tea.Cmd {
	f.seq = nextSeq()
	f.busy, f.err = true, ""
	seq, b, r := f.seq, f.b, f.r
	return f.w.do(func(ctx context.Context) tea.Msg {
		return docAnsweredMsg{seq, r, a, b.AnswerDocument(ctx, r.ID, a)}
	})
}

// decline says no to the request on the worker, as the box closes after a
// failed answer that left the request waiting. It is best effort.
func (f *docForm) decline() tea.Cmd {
	b, id := f.b, f.r.ID
	return f.w.do(func(ctx context.Context) tea.Msg {
		_ = b.AnswerDocument(ctx, id, AnswerNo)
		return nil
	})
}

// answered takes the outcome of an answer; it reports whether the box is
// done.
func (f *docForm) answered(msg docAnsweredMsg) bool {
	if msg.seq != f.seq {
		return false
	}
	f.busy = false
	if msg.err != nil {
		f.err, f.ended = msg.err.Error(), errors.Is(msg.err, ErrRequestEnded)
		f.opened = errors.Is(msg.err, ErrAllowNotSaved)
		// A No on a request that has ended has nothing to show.
		return msg.answer == AnswerNo && f.ended
	}
	return true
}

// shows reports whether the box is open on the answer msg is the outcome of.
func (f *docForm) shows(msg docAnsweredMsg) bool { return msg.seq == f.seq }

func (f *docForm) lines(width int) []string {
	what := f.r.who() + " wants to show " + clean(f.r.Doc)
	if l := clean(f.r.Label); l != "" {
		what += " (" + l + ")"
	}
	lines := wrap(what, width)
	lines = append(lines, wrap("Nearhost will serve files in "+f.root+", except dotfiles.", width)...)
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

func (f *docForm) heading() heading { return heading{title: "Show a document"} }

func (f *docForm) keys() []footerItem {
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

// gotDoc queues a document request and says it came.
func (m *Model) gotDoc(r DocRequest) tea.Cmd {
	text := fmt.Sprintf("%s wants to show %s", r.who(), clean(r.Doc))
	m.logs.note(text + ".")
	m.waiting = append(m.waiting, asked{doc: &r})
	return m.notify(text)
}

// docShown tells that a document opened without asking: in the status line
// and the log, with no terminal notification.
func (m *Model) docShown(msg docShownMsg) tea.Cmd {
	text := fmt.Sprintf("%s %s opened %s", requesterName(msg.requester), docWhere(msg.origin), clean(msg.doc))
	m.logs.note(text + ".")
	return m.showToast(text, false)
}

// docAnswered logs the outcome of an answer, whatever is open now, as
// passAnswered does, and closes the box only when that is the one that
// answered.
func (m *Model) docAnswered(msg docAnsweredMsg) tea.Cmd {
	shown := false
	if df, ok := m.form.(*docForm); ok && df.shows(msg) {
		shown = true
		if df.answered(msg) {
			m.form = nil
		}
	}
	doc, who := clean(msg.r.Doc), msg.r.who()
	switch {
	case errors.Is(msg.err, ErrRequestEnded) && (msg.answer == AnswerNo || errors.Is(msg.err, ErrAllowNotSaved)):
		m.logs.note(fmt.Sprintf("The request to show %s for %s had already ended.", doc, who))
		return nil
	case errors.Is(msg.err, ErrAllowNotSaved):
		m.logs.note(fmt.Sprintf("Opened %s for %s, but Always allow was not saved.", doc, who))
		return nil
	}
	if msg.err != nil {
		if !shown {
			verb := "show"
			if msg.answer == AnswerNo {
				verb = "decline"
			}
			m.logs.note(fmt.Sprintf("Could not %s %s for %s: %s.", verb, doc, who, strings.TrimSuffix(clean(msg.err.Error()), ".")))
		}
		return nil
	}
	line := fmt.Sprintf("Opened %s for %s.", doc, who)
	if msg.answer == AnswerNo {
		line = fmt.Sprintf("Declined to show %s for %s.", doc, who)
	}
	m.logs.note(line)
	return nil
}
