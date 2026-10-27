package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
)

// passRow is a row of a server panel's passthroughs: an open passthrough or
// an Always allow grant, one of the two.
type passRow struct {
	pass  *engine.Passthrough
	grant *grants.Grant
}

// key names the row's passthrough or grant, so the selection can follow it
// while rows come and go around it.
func (r passRow) key() string {
	if r.grant != nil {
		return "grant " + r.grant.Requester
	}
	return "pass " + r.pass.ID
}

// passRows are the rows of the server called name: its open passthroughs,
// sorted by remote port, then its grants, sorted by requester. They are read
// from the backend each time, so what the engine closes is gone at once. The
// error is why the grants could not be read, when they could not. The grants
// are those fetchGrants last read, since reading them blocks. The
// focus on the rows ends when none are left. The selected row is found
// again by its key, so a row opened or closed meanwhile does not move the
// selection onto another; when it is gone, the selection stays at its place
// within the rows.
func (m *Model) passRows(name string) ([]passRow, error) {
	passes := slices.Clone(m.b.Passthroughs(name))
	sort.SliceStable(passes, func(i, j int) bool { return passes[i].Remote < passes[j].Remote })
	var gs []grants.Grant
	var err error
	if e := m.grantCache[name]; e != nil {
		gs, err = slices.Clone(e.list), e.err
	}
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].Requester < gs[j].Requester })
	var rows []passRow
	for i := range passes {
		rows = append(rows, passRow{pass: &passes[i]})
	}
	for i := range gs {
		rows = append(rows, passRow{grant: &gs[i]})
	}
	if m.passFocus {
		if i := slices.IndexFunc(rows, func(r passRow) bool { return r.key() == m.passSel }); i >= 0 {
			m.passRow = i
		} else {
			m.passRow = max(min(m.passRow, len(rows)-1), 0)
		}
		m.passFocus = len(rows) > 0
		if m.passFocus {
			m.passSel = rows[m.passRow].key()
		}
	}
	return rows, err
}

// age is how long ago t was, as a short word: "12m".
func age(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// text is the row's line.
func (r passRow) text(now time.Time) string {
	if g := r.grant; g != nil {
		return requesterName(g.Requester) + " · since " + g.Created.Local().Format("2 Jan")
	}
	p := r.pass
	parts := []string{fmt.Sprintf("%d → localhost:%d", p.Remote, p.Local), requesterName(p.Requester)}
	if l := clean(p.Label); l != "" {
		parts = append(parts, l)
	}
	if s := clean(p.Session); s != "" {
		parts = append(parts, string([]rune(s)[:min(len([]rune(s)), 8)]))
	}
	return strings.Join(append(parts, age(now, p.Opened)), " · ")
}

// passLines draws the server's passthroughs and grants for its panel: a label
// line, then a line a row, the selected one filled while the rows have the
// focus. The labels are longer than a field's, so they have lines of their
// own.
func (m *Model) passLines(name string, width int) []string {
	rows, err := m.passRows(name)
	var lines []string
	shown := map[string]bool{}
	section := func(label string, in func(passRow) bool) {
		for i, r := range rows {
			if !in(r) {
				continue
			}
			if !shown[label] {
				lines = append(lines, styleLabel.UnsetWidth().Render(label))
				shown[label] = true
			}
			sel := m.passFocus && i == m.passRow
			for j, l := range wrap(r.text(m.now()), width-2) {
				lead, style := "  ", styleText
				if sel {
					style = style.Background(colourSelected)
					if j == 0 {
						lead = styleBeacon.Render("▌") + " "
					}
				}
				lines = append(lines, lead+style.Render(l))
			}
		}
	}
	section("passthroughs", func(r passRow) bool { return r.pass != nil })
	section("always allowed", func(r passRow) bool { return r.grant != nil })
	// A grants error is news only beside passthroughs, or with the rows
	// selected; otherwise every server panel of a run without agents would
	// carry it.
	if err != nil && (m.passFocus || slices.ContainsFunc(rows, func(r passRow) bool { return r.pass != nil })) {
		if !shown["always allowed"] {
			lines = append(lines, styleLabel.UnsetWidth().Render("always allowed"))
		}
		for _, l := range wrap(sentence(clean(err.Error())), width-2) {
			lines = append(lines, "  "+styleAmber.Render(l))
		}
	}
	return lines
}

// passKey handles a key while the server panel's passthroughs have the
// focus, and reports whether it was one of theirs.
func (m *Model) passKey(s string) (tea.Cmd, bool) {
	kind, name := m.selected()
	if kind != rowServer {
		m.passFocus = false
		return nil, false
	}
	rows, _ := m.passRows(name)
	if !m.passFocus {
		return nil, false // the rows were all closed
	}
	switch s {
	case "tab", "esc":
		m.passFocus, m.panelErr = false, ""
	case "up", "k":
		m.passRow = max(m.passRow-1, 0)
		m.passSel = rows[m.passRow].key()
	case "down", "j":
		m.passRow = min(m.passRow+1, len(rows)-1)
		m.passSel = rows[m.passRow].key()
	case "x":
		return m.closeRow(name, rows[m.passRow]), true
	default:
		return nil, false
	}
	return nil, true
}

// closeRow closes the selected passthrough, or asks before revoking the
// selected grant.
func (m *Model) closeRow(server string, r passRow) tea.Cmd {
	m.panelErr = ""
	if g := r.grant; g != nil {
		m.form = &revokeForm{b: m.b, w: m.work, server: server, requester: g.Requester}
		return nil
	}
	b, p, key := m.b, *r.pass, rowKey(rowServer, server)
	return m.work.do(func(ctx context.Context) tea.Msg {
		return passClosedMsg{key, p, b.ClosePassthrough(ctx, p.ID)}
	})
}

// passClosedMsg is the outcome of closing a passthrough from its server's
// panel, the row of key.
type passClosedMsg struct {
	key string
	p   engine.Passthrough
	err error
}

// passClosed logs a passthrough closed from the panel, or shows why it could
// not be.
func (m *Model) passClosed(msg passClosedMsg) {
	if msg.err != nil {
		m.refused(msg.key, msg.err)
		return
	}
	m.logs.noteOn(msg.key, fmt.Sprintf("Closed port %d for %s.", msg.p.Remote, requesterName(msg.p.Requester)))
}

// revokeForm asks before taking back a grant, and takes it back on yes. It is
// not a confirmForm, which edits the config file.
type revokeForm struct {
	b                 Backend
	w                 *worker
	server, requester string
	busy              bool
	err               string
	seq               int
}

// revokedMsg is the outcome of taking back a grant.
type revokedMsg struct {
	seq               int
	server, requester string
	err               error
}

// update answers y by taking the grant back, on the worker, and n or esc by
// closing the question.
func (f *revokeForm) update(msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok || f.busy {
		return nil // a revoke runs to its end
	}
	switch km.String() {
	case "y":
		f.seq = nextSeq()
		f.busy, f.err = true, ""
		seq, b, server, requester := f.seq, f.b, f.server, f.requester
		return f.w.do(func(context.Context) tea.Msg {
			return revokedMsg{seq, server, requester, b.RevokeGrant(server, requester)}
		})
	case "n", "esc":
		return closeForm
	}
	return nil
}

// lines draws what the question adds to its title.
func (f *revokeForm) lines(width int) []string {
	lines := wrap("Open passthroughs stay open.", width)
	if f.busy {
		lines = append(lines, "", busyLine("Revoking…"))
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	return lines
}

// heading is the question.
func (f *revokeForm) heading() heading {
	return heading{title: "Stop always allowing " + requesterName(f.requester) + " on " + clean(f.server) + "?"}
}

// keys are y and n, and none while the revoke runs.
func (f *revokeForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	return []footerItem{{"y", "revoke"}, {"n", "keep"}}
}

// revoked takes the outcome of a revoke: the question closes when it was
// made, and shows why not otherwise.
func (m *Model) revoked(msg revokedMsg) {
	f, ok := m.form.(*revokeForm)
	if !ok || f.seq != msg.seq {
		return
	}
	f.busy = false
	if msg.err != nil {
		f.err = msg.err.Error()
		return
	}
	m.form = nil
	if e := m.grantCache[msg.server]; e != nil {
		e.at = time.Time{} // read again at once
	}
	m.logs.noteOn(rowKey(rowServer, msg.server), fmt.Sprintf("Stopped always allowing %s.", requesterName(msg.requester)))
}

// passCounts are the lines of Home's passthroughs: how many are open on each
// server, by name; none when none are open.
func (m *Model) passCounts(width int) []string {
	n := map[string]int{}
	for _, p := range m.b.Passthroughs("") {
		n[p.Server]++
	}
	var names []string
	for s := range n {
		names = append(names, s)
	}
	sort.Strings(names)
	var parts []string
	for _, s := range names {
		parts = append(parts, fmt.Sprintf("%s %d", clean(s), n[s]))
	}
	if len(parts) == 0 {
		return nil
	}
	// The label is longer than a field's, so the values start after it.
	const label = "passthroughs"
	lead := strings.Repeat(" ", len(label)+1)
	ls := wrap(strings.Join(parts, " · "), width-len(lead))
	for i, l := range ls {
		if i == 0 {
			ls[i] = styleLabel.UnsetWidth().Render(label) + " " + l
		} else {
			ls[i] = lead + l
		}
	}
	return ls
}

// grantsEvery is how long the grants read for a server are fresh.
const grantsEvery = 5 * time.Second

// grantsEntry is the grants last read for a server.
type grantsEntry struct {
	list []grants.Grant
	err  error
	at   time.Time // when they were read; zero when they must be read again
	busy bool      // a read is running
}

// grantsMsg is the outcome of reading a server's grants.
type grantsMsg struct {
	server string
	list   []grants.Grant
	err    error
}

// fetchGrants reads the selected server's grants on the worker when they are
// not known or no longer fresh. Reading them blocks, so the panel draws
// from what was last read.
func (m *Model) fetchGrants() tea.Cmd {
	kind, name := m.selected()
	if kind != rowServer || name == config.LocalServer || m.quitting {
		return nil
	}
	if m.grantCache == nil {
		m.grantCache = map[string]*grantsEntry{}
	}
	e := m.grantCache[name]
	if e == nil {
		e = &grantsEntry{}
		m.grantCache[name] = e
	}
	if e.busy || (!e.at.IsZero() && m.now().Sub(e.at) < grantsEvery) {
		return nil
	}
	e.busy = true
	b := m.b
	return m.work.do(func(context.Context) tea.Msg {
		list, err := b.Grants(name)
		return grantsMsg{name, list, err}
	})
}

// gotGrants keeps the grants that were read.
func (m *Model) gotGrants(msg grantsMsg) {
	if m.grantCache == nil {
		m.grantCache = map[string]*grantsEntry{}
	}
	m.grantCache[msg.server] = &grantsEntry{list: msg.list, err: msg.err, at: m.now()}
}
