package report

import (
	"errors"
	"strings"

	"github.com/nearhost/nearhost/internal/safetext"
)

// LogText returns m as a block for the core log, without a time stamp
// (the log adds it):
//
//	error [port.needs-permission] HTTPS port 443 needs permission to bind.
//	  On Linux, …
//	  Fix: sudo setcap cap_net_bind_service=+ep '/path/to/nearhost'
//	  Run `nearhost doctor` to check this and the rest of what Nearhost needs.
//
// The first line has the level, the code and the context, if any, before
// the title. Under it, indented: the explanation, line by line; the
// verbatim lines, indented once more and never trimmed; each fix
// step, as "Fix: <command>" for a step with a command and as its text for
// one without; the doctor line; and the cause, when it adds to the title.
// Text is made printable. The block has no trailing newline.
func LogText(m *Message) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.Level.String())
	b.WriteString(" [" + safetext.Line(string(m.Code)) + "] ")
	b.WriteString(Headline(m))
	more := func(s string) {
		b.WriteString("\n  " + s)
	}
	for _, l := range textLines(m.Why) {
		more(l)
	}
	for _, l := range m.Verbatim {
		more("  " + verbatimLine(l))
	}
	for _, s := range m.Fix {
		if c := safetext.Line(s.Command); c != "" {
			more("Fix: " + c)
			continue
		}
		for _, l := range textLines(s.Text) {
			more(l)
		}
	}
	if m.Doctor {
		more(DoctorLine)
	}
	for _, c := range Causes(m) {
		more("Cause: " + c)
	}
	return b.String()
}

// Headline returns the context, if any, and the title, made printable: the
// one line every renderer leads a message with.
func Headline(m *Message) string {
	title := safetext.Line(m.Title)
	if c := safetext.Line(m.Context); c != "" {
		return c + ": " + title
	}
	return title
}

// verbatimLine is a line of Message.Verbatim made printable, keeping its
// indentation: it loses its control characters, line breaks among them,
// and the space at its end.
func verbatimLine(l string) string {
	return strings.TrimRight(strings.ReplaceAll(safetext.Lines(l), "\n", " "), " \t")
}

// VerbatimLines returns m's verbatim lines made printable, keeping each
// line's indentation, for a renderer.
func VerbatimLines(m *Message) []string {
	out := make([]string, len(m.Verbatim))
	for i, l := range m.Verbatim {
		out[i] = verbatimLine(l)
	}
	return out
}

// textLines returns s made printable, as its lines without the blank ones,
// each trimmed of trailing space.
func textLines(s string) []string {
	var out []string
	for _, l := range strings.Split(safetext.Lines(s), "\n") {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// Causes returns the texts of m's cause chain, made printable, outermost
// first: one line for each error in the chain whose text says something
// new. A cause is skipped when its text, compared without case or a
// closing full stop, is already part of the title, the explanation or the
// line before it, as the text of an error a message wraps often is. An
// error that joins several (errors.Join) is one line holding all their
// texts; each joined error, and the chain under it, is then walked the
// same way, so one whose text that line already holds is skipped and only
// a cause that adds something is listed.
func Causes(m *Message) []string {
	if m == nil {
		return nil
	}
	var out []string
	title, why := sameText(m.Title), sameText(m.Why)
	last := ""
	var walk func(err error)
	walk = func(err error) {
		for err != nil {
			t := safetext.Line(err.Error())
			if n := sameText(t); n != "" && !strings.Contains(title, n) && !strings.Contains(why, n) && !strings.Contains(last, n) {
				out = append(out, t)
				last = n
			}
			switch u := err.(type) {
			case interface{ Unwrap() []error }:
				for _, e := range u.Unwrap() {
					walk(e)
				}
				return
			default:
				err = errors.Unwrap(err)
			}
		}
	}
	walk(m.Cause)
	return out
}

// sameText is s as Causes compares it: printable, in lower case, its
// blanks run together, without a closing full stop.
func sameText(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(safetext.Lines(s))), " ")
	return strings.TrimSuffix(s, ".")
}
