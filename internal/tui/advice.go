package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// advice is what is known of a project's advice.
type advice struct {
	of      string // the project's config entry it was read for
	loading bool
	items   []Advisory
	notes   []string
	err     string
	failed  time.Time // when reading failed; it is tried again adviceRetry later
}

// adviceRetry is how long advice that could not be read waits before it is
// read again. Each read of a remote project's advice opens a connection.
const adviceRetry = 30 * time.Second

// adviceMsg is a project's advice, read in the background.
type adviceMsg struct {
	name, of string
	items    []Advisory
	notes    []string
	err      error
}

// entryOf identifies a project's config entry: advice read for one entry is
// read again when the entry changes.
func entryOf(p config.Project) string {
	return fmt.Sprintf("%+v", p)
}

// adviceWait is how long the selection rests on a project before its advice
// is read, so passing over projects reads nothing.
const adviceWait = 300 * time.Millisecond

// adviceWaitMsg ends the wait for the selection on the row with key to rest.
type adviceWaitMsg struct{ key string }

// waitForAdvice starts the wait before the selected project's advice is
// read, when it needs reading and no wait for it is running.
func (m *Model) waitForAdvice() tea.Cmd {
	if _, _, need := m.adviceNeeded(); !need || m.adviceWaiting == m.sel {
		return nil
	}
	key := m.sel
	m.adviceWaiting = key
	return tea.Tick(adviceWait, func(time.Time) tea.Msg { return adviceWaitMsg{key} })
}

// adviceNeeded reports whether the selected project's advice must be read:
// it is not known for the project's current config entry, or reading it
// failed adviceRetry ago or more.
func (m *Model) adviceNeeded() (name, of string, need bool) {
	kind, name := m.selected()
	if kind != rowProject || m.quitting {
		return "", "", false
	}
	p, ok := m.cfg.Project(name)
	if !ok {
		return "", "", false
	}
	of = entryOf(*p)
	if a, known := m.advice[name]; known && a.of == of && (a.err == "" || m.now().Sub(a.failed) < adviceRetry) {
		return "", "", false
	}
	return name, of, true
}

// readAdvice starts reading the advice of the selected project when it is
// needed.
func (m *Model) readAdvice() tea.Cmd {
	name, of, need := m.adviceNeeded()
	if !need {
		return nil
	}
	m.advice[name] = advice{of: of, loading: true}
	b := m.b
	return m.work.do(func(ctx context.Context) tea.Msg {
		items, notes, err := b.Advice(ctx, name)
		return adviceMsg{name, of, items, notes, err}
	})
}

// gotAdvice keeps advice that is still for the project's current entry.
func (m *Model) gotAdvice(msg adviceMsg) {
	if a := m.advice[msg.name]; a.of != msg.of {
		return // read for an entry that has changed since
	}
	a := advice{of: msg.of, items: msg.items, notes: msg.notes}
	if msg.err != nil {
		a.err, a.failed = msg.err.Error(), m.now()
	}
	m.advice[msg.name] = a
}

// adviceLinesFor draws a project's advice for its panel.
func (m *Model) adviceLinesFor(name string, width int) []string {
	a := m.advice[name]
	var lines []string
	switch {
	case a.loading, m.adviceWaiting == rowKey(rowProject, name):
		lines = append(lines, "", busyLine("Reading advice…"))
	case a.err != "":
		lines = append(lines, "")
		for _, l := range wrap(clean(sentence("advice could not be read: "+a.err)), width) {
			lines = append(lines, styleDim.Render(l))
		}
	}
	lines = append(lines, adviceLines(a.items, width)...)
	for _, n := range a.notes {
		for _, l := range wrap(clean(n), width) {
			lines = append(lines, styleDim.Render(l))
		}
	}
	return lines
}

// copyAdvice puts every snippet of a project's advice on the clipboard.
func (m *Model) copyAdvice(name string) tea.Cmd {
	var snippets []string
	for _, a := range m.advice[name].items {
		if s := strings.TrimSpace(a.Snippet); s != "" {
			snippets = append(snippets, s)
		}
	}
	key := rowKey(rowProject, name)
	if len(snippets) == 0 {
		m.logs.noteOn(key, name+" has no advice to copy.")
		return nil
	}
	m.logs.noteOn(key, "Copied the advice for "+name+".")
	return tea.SetClipboard(strings.Join(snippets, "\n\n"))
}
