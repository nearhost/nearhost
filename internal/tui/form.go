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

// textField is a one-line text input with a cursor. value is the text and
// the source of truth; cur is the cursor's place in runes, 0 to the rune
// count, so an edit never splits a multi-byte character. Code sets the text
// with set, which puts the cursor at the end; a cursor left out of range by
// a direct write to value is clamped when it is used.
type textField struct {
	value string
	cur   int
}

// set replaces the text from code and puts the cursor at its end.
func (f *textField) set(s string) {
	f.value = s
	f.cur = utf8.RuneCountInString(s)
}

// cursor is the cursor's place in runes, kept inside the text.
func (f *textField) cursor() int {
	f.cur = min(max(f.cur, 0), utf8.RuneCountInString(f.value))
	return f.cur
}

// atEnd reports whether the cursor is after the last character, the only
// place where completion works.
func (f *textField) atEnd() bool {
	return f.cursor() == utf8.RuneCountInString(f.value)
}

// offset is the byte offset of the rune at index n.
func (f *textField) offset(n int) int {
	for i := range f.value {
		if n == 0 {
			return i
		}
		n--
	}
	return len(f.value)
}

// key edits the field and reports whether the key was for it.
func (f *textField) key(k tea.KeyPressMsg) bool {
	cur := f.cursor()
	switch k.String() {
	case "left":
		f.cur = max(cur-1, 0)
		return true
	case "right":
		f.cur = cur + 1
		f.cursor()
		return true
	case "home", "ctrl+a":
		f.cur = 0
		return true
	case "end", "ctrl+e":
		f.cur = utf8.RuneCountInString(f.value)
		return true
	case "backspace":
		if cur > 0 {
			at := f.offset(cur - 1)
			f.value = f.value[:at] + f.value[f.offset(cur):]
			f.cur = cur - 1
		}
		return true
	case "delete":
		if cur < utf8.RuneCountInString(f.value) {
			f.value = f.value[:f.offset(cur)] + f.value[f.offset(cur+1):]
		}
		return true
	case "ctrl+u":
		f.set("")
		return true
	}
	if k.Text == "" {
		return false
	}
	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, k.Text)
	at := f.offset(cur)
	f.value = f.value[:at] + text + f.value[at:]
	f.cur = cur + utf8.RuneCountInString(text)
	return true
}

// view draws the field, with a cursor when it has the focus. The cursor
// after the last character is a bar; on a character it shows that character
// reversed, so the text does not shift and a wide character keeps its width.
func (f *textField) view(focused bool) string {
	if !focused {
		return clean(f.value)
	}
	at := f.offset(f.cursor())
	before, after := f.value[:at], f.value[at:]
	if after == "" {
		return clean(before) + styleBeacon.Render("▏")
	}
	r, size := utf8.DecodeRuneInString(after)
	return clean(before) + styleBeacon.Reverse(true).Render(clean(string(r))) + clean(after[size:])
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
// nothing, and what a step returns after stop is dropped, and released when
// it holds something, since the interface is going and will not take it:
// such an answer is mostly the failure stop caused, which the form would
// otherwise show while Nearhost stops.
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
		if !stopped {
			return msg
		}
		if r, ok := msg.(releaser); ok {
			r.release()
		}
		return nil
	}
}

// stop ends the running steps and waits for them until ctx ends. From now
// on what a step returns is released. unblock, when not nil, runs first, on
// the side, to end what a step may be stuck on that its context cannot
// reach, such as a read on a connection to a server that stopped answering;
// stop waits for it too. A step or unblock still running when ctx ends is
// left behind.
func (w *worker) stop(ctx context.Context, unblock func()) {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	unblocked := make(chan struct{})
	go func() {
		defer close(unblocked)
		if unblock != nil {
			unblock()
		}
	}()
	w.cancel()
	ended := make(chan struct{})
	go func() {
		w.running.Wait()
		close(ended)
	}()
	for unblocked != nil || ended != nil {
		select {
		case <-unblocked:
			unblocked = nil
		case <-ended:
			ended = nil
		case <-ctx.Done():
			return
		}
	}
}

// paste adds pasted text to the field, without line breaks or control
// characters.
func (f *textField) paste(s string) {
	f.key(tea.KeyPressMsg{Text: s})
}
