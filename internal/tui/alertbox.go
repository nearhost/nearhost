package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/report"
)

// alertBox shows an error from something the user did in this interface,
// over the screen as the quit box is: what happened, why, the steps that fix
// it with their commands, and whether `nearhost doctor` helps. Enter or Esc
// closes it, and c copies its first command. Problems that arrive on their
// own never open it: they go to the problems panel, with a toast.
type alertBox struct {
	msg *report.Message
}

// showAlert opens the alert for msg, an error from the user's own action
// on the row key, and writes it to that row's log too, where it stays once
// the alert is closed. A nil msg opens nothing.
func (m *Model) showAlert(key string, msg *report.Message) tea.Cmd {
	if msg == nil {
		return nil
	}
	m.logs.noteOn(key, messageLine(msg))
	m.alertBox = &alertBox{msg: msg}
	return nil
}

// alertKey handles a key while the alert is open.
func (m *Model) alertKey(s string) tea.Cmd {
	switch s {
	case "enter", "esc":
		m.alertBox = nil
	case "c":
		if c := m.alertBox.command(); c != "" {
			return tea.Batch(copyText(m.sel, c), m.showToast("Copied "+c+".", false))
		}
	}
	return nil
}

// command is the alert's first command, made printable, or "".
func (b *alertBox) command() string {
	for _, s := range b.msg.Fix {
		if c := oneLine(s.Command); c != "" {
			return c
		}
	}
	return ""
}

func (b *alertBox) lines(width int) []string {
	msg := b.msg
	lines := wrap(styleText.Render(headline(msg)), width)
	if msg.Why != "" {
		lines = append(lines, "")
		lines = append(lines, proseLines(msg.Why, width)...)
	}
	if v := report.VerbatimLines(msg); len(v) > 0 {
		lines = append(lines, "")
		for _, l := range v {
			lines = append(lines, "  "+styleDim.Render(l))
		}
	}
	for _, s := range msg.Fix {
		if strings.TrimSpace(s.Text) != "" {
			lines = append(lines, "")
			lines = append(lines, proseLines(s.Text, width)...)
		}
		if c := oneLine(s.Command); c != "" {
			lines = append(lines, "", "  "+styleBeacon.Render(c))
		}
	}
	if msg.Doctor {
		lines = append(lines, "")
		lines = append(lines, wrap(report.DoctorLine, width)...)
	}
	return lines
}

func (b *alertBox) keys() []footerItem {
	items := []footerItem{{"enter", "close"}}
	if b.command() != "" {
		items = append(items, footerItem{"c", "copy command"})
	}
	return items
}

func (b *alertBox) heading() heading {
	if b.msg.Level == report.Warning {
		return heading{title: "Warning"}
	}
	return heading{title: "Something went wrong"}
}

// wide draws the alert in the wide box, so a long command fits.
func (b *alertBox) wide() bool { return true }

// headline is a message's title with the context its wrappers added, such
// as "start web: Port 8080 is in use.", made printable.
func headline(msg *report.Message) string {
	title := oneLine(msg.Title)
	if c := oneLine(msg.Context); c != "" {
		return c + ": " + title
	}
	return title
}

// oneLine is s made safe to draw, as clean makes it, on one line, without
// the spaces around it.
func oneLine(s string) string { return strings.TrimSpace(clean(s)) }

// prose is s made safe to draw on one line, its paragraphs and lines
// joined by single spaces.
func prose(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }

// proseLines is text made printable and wrapped to width, a paragraph to
// each part split by a blank line, with a blank line between paragraphs.
func proseLines(text string, width int) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		p = prose(p)
		if p == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, wrap(p, width)...)
	}
	return out
}

// messageLine is a message on one line, for the log: its headline, then
// its steps, compactly, so advice such as a command to run is kept.
func messageLine(msg *report.Message) string {
	parts := []string{headline(msg)}
	parts = append(parts, stepsLine(msg, headline(msg))...)
	return strings.Join(parts, " ")
}

// stepsLine is each of msg's steps on one line, a step without a command
// that said already leaving out: its text, then its command.
func stepsLine(msg *report.Message, said string) []string {
	var out []string
	for _, s := range msg.Fix {
		text := prose(s.Text)
		c := oneLine(s.Command)
		if c == "" && (text == "" || strings.Contains(said, text)) {
			continue
		}
		if c != "" {
			text = strings.TrimSpace(text + " " + c)
		}
		out = append(out, text)
	}
	return out
}

// withContext is what an action adds in front of a message's title, before
// the context the message has already, if any.
func withContext(action, has string) string {
	if has == "" {
		return action
	}
	return action + ": " + has
}
