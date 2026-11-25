// Package report is how Nearhost tells the user about problems. Every
// error, warning and notice is a Message: what happened, why, how to fix
// it, whether `nearhost doctor` would help, and a stable code. Code that
// finds a problem builds a message and returns it (an error) or reports it
// to the context's Sink (a warning or a notice). Renderers decide how a
// message looks wherever the user sees it: the terminal (package term), the
// core log (LogText) and the APIs (MarshalJSON, WriteProblem).
//
// Message text is written for people and may quote text Nearhost did not
// write. Callers never clean it: every renderer makes it printable.
//
// The package is a leaf: it imports only the standard library and safetext,
// so any package can use it.
package report

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Level says how much a message matters.
type Level int

const (
	// Info is a notice: worth knowing, nothing to do.
	Info Level = iota
	// Warning is something that probably is not what the user wants, while
	// the work goes on.
	Warning
	// Error is something that failed.
	Error
)

// String returns "info", "warning" or "error".
func (l Level) String() string {
	switch l {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return fmt.Sprintf("level(%d)", int(l))
}

// MarshalText writes the level as its name.
func (l Level) MarshalText() ([]byte, error) {
	switch l {
	case Info, Warning, Error:
		return []byte(l.String()), nil
	}
	return nil, fmt.Errorf("report: unknown level %d", int(l))
}

// UnmarshalText reads a level's name.
func (l *Level) UnmarshalText(b []byte) error {
	switch string(b) {
	case "info":
		*l = Info
	case "warning":
		*l = Warning
	case "error":
		*l = Error
	default:
		return fmt.Errorf("report: unknown level %q", b)
	}
	return nil
}

// DoctorLine is what every renderer shows for a message that suggests
// `nearhost doctor`.
const DoctorLine = "Run `nearhost doctor` to check this and the rest of what Nearhost needs."

// Step is one thing to do to fix a problem: what it does, then the command
// that does it, if there is one.
type Step struct {
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
}

// Message is an error, a warning or a notice.
type Message struct {
	Level Level
	// Code is stable and listed in the catalog (codes.go).
	Code Code
	// Title says what happened, in one sentence.
	Title string
	// Why explains it in plain words; it may be "". Paragraphs are
	// separated by a blank line.
	Why string
	// Verbatim is text quoted line by line as it came, such as the end of a
	// log: renderers show it after Why, made printable but never wrapped or
	// trimmed, so a command in it stays whole and can be copied. It may be
	// empty.
	Verbatim []string
	// Fix is what to do, in order; it may be empty.
	Fix []Step
	// Doctor suggests running `nearhost doctor`.
	Doctor bool
	// Data holds facts a client can act on, such as "port": 443. Values
	// must marshal to JSON.
	Data map[string]any
	// Cause is what went wrong underneath; it may be nil.
	Cause error
	// Exit is the exit status of a command that returns this error; 0 means 1.
	Exit int
	// Context is the text that errors wrapping this message added, such as
	// "start web" for fmt.Errorf("start web: %w", m). From sets it.
	Context string
}

// Error returns the title, then the explanation after a blank line, so a
// message wrapped in another error still reads well.
func (m *Message) Error() string {
	if m.Why == "" {
		return m.Title
	}
	return m.Title + "\n\n" + m.Why
}

// Unwrap returns the cause, so errors.Is and errors.As see it.
func (m *Message) Unwrap() error { return m.Cause }

func newMessage(l Level, c Code, format string, a []any) *Message {
	return &Message{Level: l, Code: c, Title: fmt.Sprintf(format, a...)}
}

// Errorf returns an error-level message whose title is the formatted text.
func Errorf(c Code, format string, a ...any) *Message { return newMessage(Error, c, format, a) }

// Warnf returns a warning whose title is the formatted text.
func Warnf(c Code, format string, a ...any) *Message { return newMessage(Warning, c, format, a) }

// Notef returns a notice (level Info) whose title is the formatted text.
func Notef(c Code, format string, a ...any) *Message { return newMessage(Info, c, format, a) }

// Because sets why the problem happened. Called again, it adds a paragraph.
func (m *Message) Because(format string, a ...any) *Message {
	why := fmt.Sprintf(format, a...)
	if m.Why != "" {
		why = m.Why + "\n\n" + why
	}
	m.Why = why
	return m
}

// Quote adds lines to the text quoted verbatim (see Message.Verbatim).
func (m *Message) Quote(lines ...string) *Message {
	m.Verbatim = append(m.Verbatim, lines...)
	return m
}

// Step adds a fix step: what it does, then the command to run.
func (m *Message) Step(text, command string) *Message {
	m.Fix = append(m.Fix, Step{Text: text, Command: command})
	return m
}

// Then adds a fix step with no command.
func (m *Message) Then(text string) *Message { return m.Step(text, "") }

// SuggestDoctor suggests running `nearhost doctor`.
func (m *Message) SuggestDoctor() *Message {
	m.Doctor = true
	return m
}

// With adds a fact to the message's data.
func (m *Message) With(key string, v any) *Message {
	if m.Data == nil {
		m.Data = map[string]any{}
	}
	m.Data[key] = v
	return m
}

// Wrap sets the cause.
func (m *Message) Wrap(err error) *Message {
	m.Cause = err
	return m
}

// ExitCode sets the exit status of a command that returns this error.
func (m *Message) ExitCode(n int) *Message {
	m.Exit = n
	return m
}

// As finds the first message in err's chain.
func As(err error) (*Message, bool) {
	var m *Message
	if errors.As(err, &m) && m != nil {
		return m, true
	}
	return nil, false
}

// From returns err as a message, for a renderer. A chain that holds a
// message gives a copy of it whose Context is the text the wrappers added
// ("start web" for fmt.Errorf("start web: %w", m)); the copy's fields are
// the message's own, so wrapping never loses the code or the fix. Any other
// error becomes an Unexpected message with the error's text as its title
// and the error as its cause. From(nil) is nil.
func From(err error) *Message {
	if err == nil {
		return nil
	}
	m, ok := As(err)
	if !ok {
		return Errorf(Unexpected, "%s", err).Wrap(err)
	}
	c := *m
	c.Fix = slices.Clone(m.Fix)
	c.Verbatim = slices.Clone(m.Verbatim)
	c.Data = maps.Clone(m.Data)
	if added := wrapperText(err.Error(), m.Error()); added != "" {
		if c.Context != "" {
			added += ": " + c.Context
		}
		c.Context = added
	}
	return &c
}

// wrapperText returns what the wrappers of a message added in front of its
// text: "start web" for "start web: <inner>". Text a wrapper adds after
// the message is not kept.
func wrapperText(full, inner string) string {
	if full == inner {
		return ""
	}
	i := strings.LastIndex(full, inner)
	if i <= 0 {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(full[:i]), ":")
}
