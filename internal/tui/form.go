package tui

import (
	"strings"
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
	// keys are the footer actions of the form's current step.
	keys() []footerItem
}

// Messages a form ends with.
type (
	// formClosedMsg closes the open form without adding anything.
	formClosedMsg struct{}
	// serverAddedMsg is a server the add-server form saved.
	serverAddedMsg struct{ srv config.Server }
	// projectAddedMsg is a project the add-project form saved.
	projectAddedMsg struct{ name string }
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
