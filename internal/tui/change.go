package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// changeForm reviews what a project's .nearhostrc asks for that waits for
// approval, laid out as confirmForm is: y approves it, n declines it, and esc
// leaves it waiting.
type changeForm struct {
	b      Backend
	w      *worker
	name   string
	change *engine.Change
	items  []approvalItem // the hosts to approve, with their internet addresses once known
	moves  []string       // what is served now that the change moves or removes
	busy   bool
	err    string
	seq    int
}

type (
	// changeAddressesMsg is the internet addresses of a change's hosts.
	changeAddressesMsg struct {
		seq   int
		items []approvalItem
	}
	// changeAcceptedMsg is the outcome of approving a change.
	changeAcceptedMsg struct {
		seq int
		err error
	}
	// changeDeclinedMsg closes the review after a change was declined.
	changeDeclinedMsg struct{ name string }
)

// reviewChange opens the review of the change the project called name waits
// on, and looks up the internet addresses of its hosts. It opens nothing when
// no change waits.
func (m *Model) reviewChange(name string) tea.Cmd {
	change := m.b.Status(name).Pending
	if change == nil {
		return nil
	}
	f := &changeForm{b: m.b, w: m.work, name: name, change: change, seq: nextSeq()}
	for _, it := range change.Items {
		f.items = append(f.items, approvalItem{Item: it})
	}
	if p, ok := m.cfg.Project(name); ok {
		f.moves = moves(*p, change.Project)
	}
	m.form = f
	b, seq, items := m.b, f.seq, change.Items
	return f.w.do(func(ctx context.Context) tea.Msg {
		out := make([]approvalItem, len(items))
		for i, it := range items {
			addr, _ := b.PublicAddress(ctx, it.Host)
			out[i] = approvalItem{it, addr}
		}
		return changeAddressesMsg{seq, out}
	})
}

func (f *changeForm) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case changeAddressesMsg:
		if msg.seq == f.seq {
			f.items = msg.items
		}
	case changeAcceptedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = false
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		note, sel := "Approved what "+nearhostrc.FileName+" asks for.", rowKey(rowProject, f.name)
		return func() tea.Msg { return changedMsg{note, sel} }
	case tea.KeyPressMsg:
		if f.busy {
			return nil // an approval runs to its end
		}
		switch msg.String() {
		case "y":
			f.seq = nextSeq()
			f.busy, f.err = true, ""
			// The change shown is the one approved: AcceptChange refuses it
			// when the file has changed again since.
			seq, b, name, sha := f.seq, f.b, f.name, f.change.SHA256
			return f.w.do(func(context.Context) tea.Msg { return changeAcceptedMsg{seq, b.AcceptChange(name, sha)} })
		case "n":
			f.b.DeclineChange(f.name, f.change.SHA256)
			name := f.name
			return func() tea.Msg { return changeDeclinedMsg{name} }
		case "esc":
			return closeForm
		}
	}
	return nil
}

func (f *changeForm) lines(width int) []string {
	lines := wrap("Its "+nearhostrc.FileName+" changed. Until you approve it, the project keeps serving what was approved before.", width)
	lines = append(lines, approvalLines(f.items, width)...)
	if len(f.moves) > 0 {
		lines = append(lines, "", "It also moves or removes what is served now:")
		for _, mv := range f.moves {
			lines = append(lines, wrap("  "+mv, width)...)
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

func (f *changeForm) heading() heading {
	return heading{title: "Review " + nearhostrc.FileName + " for " + clean(f.name)}
}

func (f *changeForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	return []footerItem{{"y", "approve"}, {"n", "decline"}, {"esc", "later"}}
}

// moves says, a line each as was → now, what the change from old to next
// takes away from what old serves: a host moved to another port or gone, and
// a route whose port is gone, unless its hosts moved with it.
func moves(old, next config.Project) []string {
	var out []string
	for _, r := range old.Routes {
		if r.Role == config.RoleIgnore {
			continue
		}
		moved := false
		for _, h := range r.Hosts {
			if r.Role != config.RoleWeb {
				break
			}
			switch now := portOf(next, h); {
			case now == 0:
				out = append(out, fmt.Sprintf("%s port %d → removed", clean(h), r.Port))
			case now != r.Port:
				moved = true
				out = append(out, fmt.Sprintf("%s port %d → %d", clean(h), r.Port, now))
			}
		}
		gone := !slices.ContainsFunc(next.Routes, func(q config.Route) bool { return q.Port == r.Port })
		if gone && !moved {
			what := "port " + strconv.Itoa(r.Port)
			if r.Label != "" {
				what += " (" + clean(r.Label) + ")"
			}
			out = append(out, what+" → removed")
		}
	}
	return out
}

// portOf is the port of the web route of p that serves host, or 0.
func portOf(p config.Project, host string) int {
	for _, r := range p.Routes {
		if r.Role != config.RoleWeb {
			continue
		}
		for _, h := range r.Hosts {
			if strings.EqualFold(h, host) {
				return r.Port
			}
		}
	}
	return 0
}

// pendingLines say that a project's .nearhostrc waits for approval, or why it
// could not be taken into account.
func pendingLines(st engine.ProjectStatus, width int) []string {
	var lines []string
	if st.Pending != nil {
		hosts := "1 host that needs"
		if n := len(st.Pending.Items); n != 1 {
			hosts = strconv.Itoa(n) + " hosts that need"
		}
		text := nearhostrc.FileName + " asks for " + hosts + " approval · a to review"
		for _, l := range wrap(text, width) {
			lines = append(lines, styleAmber.Render(l))
		}
	}
	if st.Notice != "" {
		for _, l := range wrap(clean(st.Notice), width) {
			lines = append(lines, styleDim.Render(l))
		}
	}
	return lines
}

// recheck reads the project's .nearhostrc again.
func (m *Model) recheck(name string) tea.Cmd {
	m.panelErr = ""
	b := m.b
	return m.work.do(func(ctx context.Context) tea.Msg {
		return recheckedMsg{name, b.Recheck(ctx, name)}
	})
}

// recheckedMsg is the outcome of reading a project's .nearhostrc again.
type recheckedMsg struct {
	name string
	err  error
}

// enableClash refuses to start the disabled project p when it serves a host
// another enabled project serves, as saving it enabled would be refused.
func (m *Model) enableClash(p config.Project) error {
	if p.Enabled {
		return nil
	}
	p.Enabled = true
	return hostClash(m.cfg, p)
}
