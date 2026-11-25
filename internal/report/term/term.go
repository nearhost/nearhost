// Package term renders report messages for a terminal:
//
//	Error: The service cannot run Nearhost from this copy yet.
//
//	On Linux, a program may use ports below 1024, …
//
//	To give it permission, run:
//
//	  sudo setcap cap_net_bind_service=+ep '/path/to/nearhost'
//
//	Run `nearhost doctor` to check this and the rest of what Nearhost needs.
//
// Prose wraps to the terminal's width (at most a comfortable 72 columns);
// commands never wrap, so they can be copied. Importing the package makes
// it report.Fallback, writing to standard error.
package term

import (
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	xterm "golang.org/x/term"

	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/safetext"
)

func init() {
	report.Fallback = report.SinkFunc(func(m *report.Message) {
		Render(os.Stderr, m, For(os.Stderr, false))
	})
}

// Options says how to render.
type Options struct {
	// Width is the terminal's width in columns; 0 means 80.
	Width int
	// Color adds colour: the label red, yellow or dim, commands highlighted.
	Color bool
	// Verbose shows the code and the cause chain.
	Verbose bool
}

// measure is the widest prose is wrapped to, however wide the terminal.
const measure = 72

const defaultWidth = 80

// For returns the options for writing to f: its width and colour when it is
// a terminal, colour only when NO_COLOR is unset and TERM is not dumb.
func For(f *os.File, verbose bool) Options {
	if f == nil {
		return options(false, 0, os.Getenv, verbose)
	}
	fd := int(f.Fd())
	tty := xterm.IsTerminal(fd)
	width := 0
	if tty {
		width, _, _ = xterm.GetSize(fd)
		tty = enableColor(f)
	}
	return options(tty, width, os.Getenv, verbose)
}

// options decides the options for a writer that is a terminal or not.
func options(tty bool, width int, getenv func(string) string, verbose bool) Options {
	if width <= 0 {
		width = defaultWidth
	}
	return Options{
		Width:   width,
		Color:   tty && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb",
		Verbose: verbose,
	}
}

var (
	errorLabel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Red)
	warningLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Yellow)
	noteLabel    = lipgloss.NewStyle().Faint(true)
	commandStyle = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	dimStyle     = lipgloss.NewStyle().Faint(true)
)

// Render writes m to w. Every text in it is made printable first.
func Render(w io.Writer, m *report.Message, o Options) {
	if m == nil {
		return
	}
	width := o.Width
	if width <= 0 {
		width = defaultWidth
	}
	width = min(width, measure)
	style := func(s lipgloss.Style, text string) string {
		if !o.Color {
			return text
		}
		return s.Render(text)
	}

	label, labelStyle := "Note:", noteLabel
	switch m.Level {
	case report.Error:
		label, labelStyle = "Error:", errorLabel
	case report.Warning:
		label, labelStyle = "Warning:", warningLabel
	}
	title := wrap(label+" "+headline(m), width)
	title[0] = style(labelStyle, label) + strings.TrimPrefix(title[0], label)
	first := strings.Join(title, "\n")
	if o.Verbose && m.Code != "" {
		first += "\n" + style(dimStyle, "("+safetext.Line(string(m.Code))+")")
	}
	paras := []string{first}

	paras = append(paras, prose(m.Why, width)...)
	if lines := report.VerbatimLines(m); len(lines) > 0 {
		// Quoted as it came, never wrapped, so a command in it can be copied.
		for i, l := range lines {
			lines[i] = "  " + l
		}
		paras = append(paras, strings.Join(lines, "\n"))
	}
	for _, s := range m.Fix {
		paras = append(paras, prose(s.Text, width)...)
		if c := safetext.Line(s.Command); c != "" {
			paras = append(paras, "  "+style(commandStyle, c))
		}
	}
	if m.Doctor {
		paras = append(paras, strings.Join(wrap(report.DoctorLine, width), "\n"))
	}
	if o.Verbose {
		var causes []string
		for _, c := range report.Causes(m) {
			causes = append(causes, style(dimStyle, "Cause: "+c))
		}
		if len(causes) > 0 {
			paras = append(paras, strings.Join(causes, "\n"))
		}
	}
	io.WriteString(w, strings.Join(paras, "\n\n")+"\n")
}

// headline returns the context, if any, and the title, made printable.
func headline(m *report.Message) string {
	title := safetext.Line(m.Title)
	if c := safetext.Line(m.Context); c != "" {
		return c + ": " + title
	}
	return title
}

// prose returns s made printable as wrapped paragraphs. Paragraphs are
// separated by a blank line in s; a single line break is kept.
func prose(s string, width int) []string {
	var out []string
	for _, p := range strings.Split(safetext.Lines(s), "\n\n") {
		var lines []string
		for _, l := range strings.Split(p, "\n") {
			l = strings.TrimSpace(strings.ReplaceAll(l, "\t", " "))
			if l != "" {
				lines = append(lines, wrap(l, width)...)
			}
		}
		if len(lines) > 0 {
			out = append(out, strings.Join(lines, "\n"))
		}
	}
	return out
}

// wrap breaks s into lines no wider than width at spaces. A word wider than
// width stays whole on a line of its own.
func wrap(s string, width int) []string {
	var lines []string
	var cur strings.Builder
	curWidth := 0
	for _, word := range strings.Fields(s) {
		ww := lipgloss.Width(word)
		if curWidth > 0 && curWidth+1+ww > width {
			lines = append(lines, cur.String())
			cur.Reset()
			curWidth = 0
		}
		if curWidth > 0 {
			cur.WriteByte(' ')
			curWidth++
		}
		cur.WriteString(word)
		curWidth += ww
	}
	if curWidth > 0 || len(lines) == 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
