package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// confirmForm asks before a change to the config file, such as removing a
// project, and makes it on yes.
type confirmForm struct {
	b     Backend
	w     *worker
	ask   []string                       // the question, a line each
	edit  func(cfg *config.Config) error // the change
	note  string                         // logged once it is made
	after string                         // the row selected afterwards
	busy  bool
	err   string
	seq   int
}

type (
	// confirmedMsg is the outcome of the change.
	confirmedMsg struct {
		seq int
		err error
	}
	// changedMsg closes the form after the change was made.
	changedMsg struct{ note, sel string }
)

func (f *confirmForm) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case confirmedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = false
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		note, sel := f.note, f.after
		return func() tea.Msg { return changedMsg{note, sel} }
	case tea.KeyPressMsg:
		if f.busy {
			return nil // a change runs to its end
		}
		switch msg.String() {
		case "y":
			f.seq = nextSeq()
			f.busy, f.err = true, ""
			seq, b, edit := f.seq, f.b, f.edit
			return f.w.do(func(context.Context) tea.Msg { return confirmedMsg{seq, b.EditConfig(edit)} })
		case "n", "esc":
			return closeForm
		}
	}
	return nil
}

func (f *confirmForm) lines(width int) []string {
	var lines []string
	for i, a := range f.ask {
		for _, l := range wrap(clean(a), width) {
			if i == 0 {
				l = styleTitle.Render(l)
			}
			lines = append(lines, l)
		}
		if i == 0 {
			lines = append(lines, "")
		}
	}
	if f.busy {
		lines = append(lines, "", busyLine("Saving…"))
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	return lines
}

func (f *confirmForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	return []footerItem{{"y", "remove"}, {"n", "keep"}}
}

// removeProject asks before removing the project called name.
func (m *Model) removeProject(p config.Project) {
	name := p.Name
	m.form = &confirmForm{
		b: m.b, w: m.work,
		ask: []string{"Remove " + name + "?", "It stops now and leaves the config."},
		edit: func(cfg *config.Config) error {
			for i, q := range cfg.Projects {
				if q.Name == name {
					cfg.Projects = append(cfg.Projects[:i], cfg.Projects[i+1:]...)
					return nil
				}
			}
			return errNoLongerConfigured(name)
		},
		note: "Removed " + name + ".", after: rowKey(rowServer, p.Server),
	}
}

// removeServer asks before removing a server and every project on it.
func (m *Model) removeServer(name string) {
	var on []string
	for _, p := range m.cfg.Projects {
		if p.Server == name {
			on = append(on, p.Name)
		}
	}
	ask := []string{"Remove " + name + "?"}
	if len(on) > 0 {
		ask = append(ask, "Its projects go with it and stop now: "+strings.Join(on, ", ")+".")
	}
	m.form = &confirmForm{
		b: m.b, w: m.work, ask: ask,
		edit: func(cfg *config.Config) error {
			found := false
			var servers []config.Server
			for _, s := range cfg.Servers {
				if s.Name == name {
					found = true
					continue
				}
				servers = append(servers, s)
			}
			if !found {
				return errNoLongerConfigured(name)
			}
			var projects []config.Project
			for _, p := range cfg.Projects {
				if p.Server != name {
					projects = append(projects, p)
				}
			}
			cfg.Servers, cfg.Projects = servers, projects
			return nil
		},
		note: "Removed " + name + ".", after: homeKey,
	}
}
