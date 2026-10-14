package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// The steps of the add-server form.
const (
	serverStepHost = iota
	serverStepName
	serverStepCheck
)

// serverForm adds a server: its SSH host, a name, then a check that it can
// be reached before it is saved.
type serverForm struct {
	b     Backend
	cfg   *config.Config
	step  int
	host  textField
	name  textField
	known []string // hosts from the SSH config not yet configured
	pick  int      // index into the shown hosts; -1 for the typed text
	err   string
	busy  bool
	fail  string // why the check failed
	seq   int    // the background step whose answer is awaited
	w     *worker
}

// serverCheckedMsg is the outcome of checking a server, and serverSavedMsg
// of saving it. Saving is a step of its own, taken only while the form still
// waits for the check, so a check that was left with esc saves nothing.
type (
	serverCheckedMsg struct {
		seq int
		srv config.Server
		err error
	}
	serverSavedMsg struct {
		seq int
		srv config.Server
		err error
	}
)

func newServerForm(b Backend, cfg *config.Config, w *worker) *serverForm {
	f := &serverForm{b: b, cfg: cfg, pick: -1, w: w}
	configured := map[string]bool{}
	for _, s := range cfg.Servers {
		configured[s.SSHHost] = true
		configured[s.Name] = true
	}
	for _, h := range b.SSHHosts() {
		if !configured[h] {
			f.known = append(f.known, h)
		}
	}
	return f
}

// shown is the known hosts that match what is typed.
func (f *serverForm) shown() []string {
	want := strings.ToLower(f.host.value)
	var out []string
	for _, h := range f.known {
		if strings.Contains(strings.ToLower(h), want) {
			out = append(out, h)
		}
	}
	return out
}

// hostName is the name a server reached at host gets unless one is typed:
// the host without user@ and :port.
func hostName(host string) string {
	if _, after, ok := strings.Cut(host, "@"); ok {
		host = after
	}
	if before, _, ok := strings.Cut(host, ":"); ok {
		host = before
	}
	return host
}

func (f *serverForm) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case serverCheckedMsg:
		if msg.seq != f.seq {
			return nil // the step was left
		}
		if msg.err != nil {
			f.busy, f.fail = false, msg.err.Error()
			return nil
		}
		seq, srv, b := f.seq, msg.srv, f.b
		return f.w.do(func(context.Context) tea.Msg { return serverSavedMsg{seq, srv, b.AddServer(srv)} })
	case serverSavedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = false
		if msg.err != nil {
			f.fail = msg.err.Error()
			return nil
		}
		srv := msg.srv
		return func() tea.Msg { return serverAddedMsg{srv} }
	case tea.KeyPressMsg:
		return f.key(msg)
	case tea.PasteMsg:
		switch f.step {
		case serverStepHost:
			f.host.paste(msg.Content)
			f.pick, f.err = -1, ""
		case serverStepName:
			f.name.paste(msg.Content)
			f.err = ""
		}
	}
	return nil
}

func (f *serverForm) key(k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "esc" {
		return f.back()
	}
	switch f.step {
	case serverStepHost:
		switch k.String() {
		case "up":
			f.pick = max(f.pick-1, -1)
			return nil
		case "down":
			f.pick = min(f.pick+1, len(f.shown())-1)
			return nil
		case "enter":
			host := strings.TrimSpace(f.host.value)
			if shown := f.shown(); f.pick >= 0 && f.pick < len(shown) {
				host = shown[f.pick]
			}
			if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t") {
				f.err = "an SSH host cannot be empty, start with - or contain spaces"
				return nil
			}
			f.host.value, f.err = host, ""
			f.name.value = hostName(host)
			f.step = serverStepName
			return nil
		}
		if f.host.key(k) {
			f.pick, f.err = -1, ""
		}
	case serverStepName:
		if k.String() == "enter" {
			name := strings.TrimSpace(f.name.value)
			switch _, taken := f.cfg.Server(name); {
			case name == "":
				f.err = "a server needs a name"
			case name == config.LocalServer:
				f.err = "the name local is kept for this machine"
			case taken:
				f.err = "a server called " + name + " already exists"
			default:
				f.name.value, f.err = name, ""
				f.step = serverStepCheck
				return f.check()
			}
			return nil
		}
		if f.name.key(k) {
			f.err = ""
		}
	case serverStepCheck:
		if k.String() == "r" && !f.busy {
			return f.check()
		}
	}
	return nil
}

// check connects to the server; the answer saves it when that worked.
func (f *serverForm) check() tea.Cmd {
	f.seq = nextSeq()
	f.busy, f.fail = true, ""
	seq, b := f.seq, f.b
	srv := config.Server{Name: f.name.value, SSHHost: f.host.value}
	return f.w.do(func(ctx context.Context) tea.Msg {
		return serverCheckedMsg{seq, srv, b.CheckServer(ctx, srv)}
	})
}

// back goes back a step, or closes the form from the first.
func (f *serverForm) back() tea.Cmd {
	f.err = ""
	switch f.step {
	case serverStepHost:
		return closeForm
	case serverStepName:
		f.step = serverStepHost
	case serverStepCheck:
		f.seq = nextSeq() // what the check says now is not wanted
		f.busy, f.fail = false, ""
		f.step = serverStepName
	}
	return nil
}

func (f *serverForm) lines(width int) []string {
	lines := []string{styleTitle.Render("Add server"), ""}
	lines = append(lines, field("host", f.host.view(f.step == serverStepHost), width)...)
	if f.step == serverStepHost {
		for i, h := range f.shown() {
			if i == 8 {
				break
			}
			mark := "  "
			if i == f.pick {
				mark = styleBeacon.Render("▸ ")
			}
			lines = append(lines, strings.Repeat(" ", labelWidth)+mark+clean(h))
		}
	} else {
		lines = append(lines, field("name", f.name.view(f.step == serverStepName), width)...)
	}
	if f.step == serverStepCheck {
		lines = append(lines, "")
		switch {
		case f.busy:
			lines = append(lines, busyLine("Connecting to "+clean(f.host.value)+"…"))
		case f.fail != "":
			lines = append(lines, errorLines(f.fail, width)...)
		}
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	return lines
}

func (f *serverForm) keys() []footerItem {
	switch f.step {
	case serverStepHost:
		return []footerItem{{"enter", "next"}, {"↑↓", "choose"}, {"esc", "cancel"}}
	case serverStepName:
		return []footerItem{{"enter", "check"}, {"esc", "back"}}
	}
	if f.fail != "" {
		return []footerItem{{"r", "check again"}, {"esc", "back"}}
	}
	return []footerItem{{"esc", "back"}}
}
