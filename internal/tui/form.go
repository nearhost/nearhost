package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// form is an add form. While one is open it takes the main panel and the
// keys; it says it is done with a message (serverAddedMsg, formClosedMsg,
// projectAddedMsg).
type form interface {
	update(msg tea.Msg) tea.Cmd
	// lines draws the form, width cells wide.
	lines(width int) []string
	// keys are the actions of the form's current step.
	keys() []footerItem
	// heading names the form and its current step.
	heading() heading
}

// heading is the top of a form's box: its title, its step, and the step's
// place among the form's steps (of is 0 for a form without steps).
type heading struct {
	title, step string
	at, of      int
}

// Messages a form ends with.
type (
	// formClosedMsg closes the open form without adding anything.
	formClosedMsg struct{}
	// serverAddedMsg is a server the add-server form saved.
	serverAddedMsg struct {
		srv    config.Server
		edited bool // saved over an existing server
	}
	// projectAddedMsg is a project the add-project form saved.
	projectAddedMsg struct {
		name   string
		edited bool // saved over an existing project
	}
)

func closeForm() tea.Msg { return formClosedMsg{} }

// textField is a one-line text input, edited at its end.
type textField struct {
	value string
}

// key edits the field and reports whether the key was for it.
func (f *textField) key(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "backspace":
		if _, size := utf8.DecodeLastRuneInString(f.value); size > 0 {
			f.value = f.value[:len(f.value)-size]
		}
		return true
	case "ctrl+u":
		f.value = ""
		return true
	}
	if k.Text == "" {
		return false
	}
	f.value += strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, k.Text)
	return true
}

// view draws the field, with a cursor when it has the focus.
func (f textField) view(focused bool) string {
	if focused {
		return clean(f.value) + styleBeacon.Render("▏")
	}
	return clean(f.value)
}

// busyLine is the line a form shows while it waits for a background step.
func busyLine(text string) string {
	return styleAmber.Render("◐ ") + text
}

// errorLines are a form's error, in red, wrapped to width.
func errorLines(err string, width int) []string {
	var out []string
	for _, l := range wrap(clean(sentence(err)), width) {
		out = append(out, styleRed.Render(l))
	}
	return out
}

// sentence starts s with a capital letter, as the forms show errors.
func sentence(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// lastSeq numbers the forms' background steps. One count serves every form,
// so the answer of a step in a form that has closed never matches a step of
// the next one.
var lastSeq atomic.Int64

func nextSeq() int { return int(lastSeq.Add(1)) }

// worker runs the forms' background steps under a context that quitting
// ends, and lets quitting wait for those that are running.
type worker struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	running sync.WaitGroup
	stopped bool
}

func newWorker() *worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &worker{ctx: ctx, cancel: cancel}
}

// releaser is an answer holding something that must be let go of when
// nobody takes the answer, such as a connection.
type releaser interface{ release() }

// do runs fn as a background step. A step that begins after stop does
// nothing, and what a step returns after stop is released, since the
// interface is going and will not take it.
func (w *worker) do(fn func(ctx context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		w.mu.Lock()
		if w.stopped {
			w.mu.Unlock()
			return nil
		}
		w.running.Add(1)
		w.mu.Unlock()
		defer w.running.Done()
		msg := fn(w.ctx)
		w.mu.Lock()
		stopped := w.stopped
		w.mu.Unlock()
		if r, ok := msg.(releaser); ok && stopped {
			r.release()
			return nil
		}
		return msg
	}
}

// stop ends the running steps and waits for them.
func (w *worker) stop() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	w.cancel()
	w.running.Wait()
}

// paste adds pasted text to the field, without line breaks or control
// characters.
func (f *textField) paste(s string) {
	f.key(tea.KeyPressMsg{Text: s})
}
