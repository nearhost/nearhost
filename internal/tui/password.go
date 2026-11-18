package tui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Answers of the password box, beside what the check itself returns.
var (
	// ErrWrongPassword is what a check returns for a password it refused;
	// the box then asks again.
	ErrWrongPassword = errors.New("sorry, try again")
	// ErrPasswordCancelled is the answer when the user closed the box, or
	// quit, without giving the password.
	ErrPasswordCancelled = errors.New("the password was cancelled")
	// ErrThreeWrong is the answer after three refused passwords.
	ErrThreeWrong = errors.New("three wrong passwords; nothing was changed")
)

// passwordTries is how many passwords the box takes before it gives up, as
// sudo does.
const passwordTries = 3

// passwordMsg asks for the user's password: why says what it is for, check
// is given each password typed, and done gets the answer.
type passwordMsg struct {
	why   string
	check func(ctx context.Context, password string) error
	done  chan<- error
}

// passwordGoneMsg takes away the box of a request nobody waits for any more.
type passwordGoneMsg struct{ done chan<- error }

// passwordCheckedMsg is the outcome of checking a password.
type passwordCheckedMsg struct {
	seq int
	err error
}

// passwordBox asks for the user's password over whatever is on screen. What
// is typed is shown as dots and kept only until it is checked.
type passwordBox struct {
	why   string
	input textinput.Model
	check func(ctx context.Context, password string) error
	done  chan<- error
	busy  bool
	// answered is set once done has its answer: the box takes nothing more
	// before it closes, and never answers twice.
	answered bool
	err      string
	tries    int
	seq      int
	w        *worker
	// stopCheck ends the context of the check in flight; nil when none runs.
	stopCheck context.CancelFunc
}

func newPasswordBox(msg passwordMsg, w *worker) *passwordBox {
	in := newFilter()
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.CharLimit = 256
	in.Focus()
	return &passwordBox{why: msg.why, input: in, check: msg.check, done: msg.done, w: w}
}

// releaseCheck frees the context of the check that has returned.
func (b *passwordBox) releaseCheck() {
	if b.stopCheck != nil {
		b.stopCheck()
		b.stopCheck = nil
	}
}

// cancel ends the box as cancelled: it ends the check in flight, which
// stops sudo, and answers the requester. The password the check was given
// is a string the check holds until it returns, and Go cannot overwrite
// it; the box drops its own copy, and a late result is ignored.
func (b *passwordBox) cancel() {
	b.busy = false
	b.answer(ErrPasswordCancelled)
}

// answer ends the box with err, once.
func (b *passwordBox) answer(err error) {
	b.input.SetValue("")
	b.releaseCheck() // a check still running stops, whoever ends the box
	if b.answered {
		return
	}
	b.answered = true
	b.done <- err
}

func (b *passwordBox) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case passwordCheckedMsg:
		// A result of a check the user cancelled is dropped, whatever it says.
		if msg.seq != b.seq || b.answered {
			return nil
		}
		b.busy = false
		b.releaseCheck()
		switch {
		case msg.err == nil:
			b.answer(nil)
			return b.close()
		case errors.Is(msg.err, ErrWrongPassword):
			b.tries++
			if b.tries >= passwordTries {
				b.answer(ErrThreeWrong)
				return b.close()
			}
			b.err = "Sorry, try again."
			return nil
		}
		b.answer(msg.err)
		return b.close()
	case tea.PasteMsg:
		if !b.busy && !b.answered {
			// A password copied with its line break is the password alone.
			b.input.SetValue(b.input.Value() + strings.TrimRight(msg.Content, "\r\n"))
		}
	case tea.KeyPressMsg:
		if b.answered {
			return nil
		}
		if b.busy {
			// Only esc acts during the check: it ends the check, and
			// whatever the check returns later is ignored.
			if msg.String() == "esc" {
				b.cancel()
				return b.close()
			}
			return nil
		}
		switch msg.String() {
		case "esc":
			b.answer(ErrPasswordCancelled)
			return b.close()
		case "enter":
			password := b.input.Value()
			b.input.SetValue("")
			b.busy, b.err = true, ""
			b.seq = nextSeq()
			seq, check := b.seq, b.check
			ctx, stop := context.WithCancel(b.w.ctx)
			b.stopCheck = stop
			return b.w.do(func(context.Context) tea.Msg {
				return passwordCheckedMsg{seq, check(ctx, password)}
			})
		}
		var cmd tea.Cmd
		b.input, cmd = b.input.Update(msg)
		return cmd
	}
	return nil
}

func (b *passwordBox) lines(width int) []string {
	// Between words only: a reason ends in a path more often than not.
	lines := strings.Split(ansi.Wrap(clean(b.why), width, ""), "\n")
	lines = append(lines, "")
	lines = append(lines, field("password", b.input.View(), width)...)
	if b.busy {
		lines = append(lines, "", busyLine("Checking…"))
	}
	if b.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(b.err, width)...)
	}
	return lines
}

func (b *passwordBox) keys() []footerItem {
	if b.busy {
		return []footerItem{{"esc", "cancel"}}
	}
	return []footerItem{{"enter", "continue"}, {"esc", "cancel"}}
}

func (b *passwordBox) heading() heading { return heading{title: "Password needed"} }

// passwordClosedMsg closes a password box. It names the box: the box
// answers first, so the next request can open its own box before this
// message arrives, and must not be closed by it.
type passwordClosedMsg struct{ box *passwordBox }

func (b *passwordBox) close() tea.Cmd {
	return func() tea.Msg { return passwordClosedMsg{b} }
}

// askPassword opens the password box, or answers at once while quitting.
func (m *Model) askPassword(msg passwordMsg) {
	if m.quitting {
		msg.done <- errQuitting
		return
	}
	m.password = newPasswordBox(msg, m.work)
}
