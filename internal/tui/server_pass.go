package tui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
)

// passRow is a row of a server panel's agent sections: an open
// passthrough, a served tree of documents or an Always allow grant, one of
// the three.
type passRow struct {
	pass  *engine.Passthrough
	tree  *documents.Tree
	grant *grants.Grant
}

// key names the row's passthrough, tree or grant, so the selection can
// follow it while rows come and go around it.
func (r passRow) key() string {
	switch {
	case r.grant != nil:
		return "grant " + r.grant.Kind + " " + r.grant.Requester
	case r.tree != nil:
		return "tree " + r.tree.ID
	}
	return "pass " + r.pass.ID
}

// kindOrder places passthrough grants before documents grants, and any
// other kind after both.
func kindOrder(kind string) int {
	switch kind {
	case grants.KindPassthrough:
		return 0
	case grants.KindDocuments:
		return 1
	}
	return 2
}

// passRows are the rows of the server called name: its open passthroughs,
// sorted by remote port, then its served trees, newest first, then its
// grants, sorted by requester and kind. They are read from the backend
// each time, so what the engine closes is gone at once. The
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
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].Requester != gs[j].Requester {
			return gs[i].Requester < gs[j].Requester
		}
		return kindOrder(gs[i].Kind) < kindOrder(gs[j].Kind)
	})
	trees := m.b.Documents(name)
	var rows []passRow
	for i := range passes {
		rows = append(rows, passRow{pass: &passes[i]})
	}
	for i := range trees {
		rows = append(rows, passRow{tree: &trees[i]})
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

// rowText is the row's line.
func (m *Model) rowText(r passRow) string {
	now := m.now()
	if g := r.grant; g != nil {
		return requesterName(g.Requester) + " · " + clean(g.Kind) + " · since " + g.Created.Local().Format("2 Jan")
	}
	if tr := r.tree; tr != nil {
		return m.treeText(*tr)
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

// passLines draws the server's passthroughs, trees and grants for its
// panel, in at most room lines where it can: a label line, then a line a
// row, the
// selected one filled while the rows have the focus. The labels are longer
// than a field's, so they have lines of their own. When the rows do not
// fit, a window of them that holds the selection is shown, as many rows as
// fit and at least the selected one, and a dim "+N more" line says how many
// are hidden above it, under the first row's label, and below it.
func (m *Model) passLines(name string, width, room int) []string {
	rows, err := m.passRows(name)
	// A grants error is news only beside passthroughs or trees, or with
	// the rows selected; otherwise every server panel of a run without
	// agents would carry it.
	var errLines []string
	if err != nil && (m.passFocus || slices.ContainsFunc(rows, func(r passRow) bool { return r.pass != nil || r.tree != nil })) {
		if !slices.ContainsFunc(rows, func(r passRow) bool { return r.grant != nil }) {
			errLines = append(errLines, styleLabel.UnsetWidth().Render("always allowed"))
		}
		for _, l := range wrap(sentence(clean(err.Error())), width-2) {
			errLines = append(errLines, "  "+styleAmber.Render(l))
		}
	}
	label := func(r passRow) string {
		switch {
		case r.grant != nil:
			return "always allowed"
		case r.tree != nil:
			return "documents"
		}
		return "passthroughs"
	}
	drawn := make([][]string, len(rows))
	labels, total := map[string]bool{}, len(errLines)
	for i, r := range rows {
		sel := m.passFocus && i == m.passRow
		for j, l := range wrap(m.rowText(r), width-2) {
			lead, style := "  ", styleText
			if sel {
				style = style.Background(colourSelected)
				if j == 0 {
					lead = styleBeacon.Render("▌") + " "
				}
			}
			drawn[i] = append(drawn[i], lead+style.Render(l))
		}
		if !labels[label(r)] {
			labels[label(r)] = true
			total++
		}
		total += len(drawn[i])
	}
	from, to := 0, len(rows)
	if total > room && len(rows) > 0 {
		from, to = m.passWindow(drawn, room-(total-lineCount(drawn))-2)
	}
	// The marker above the rows is left out when it, and the marker below
	// them, would push the rows shown, and so the selected one, past the
	// panel's end.
	below := 0
	if to < len(rows) {
		below = 1
	}
	above := from > 0
	if above {
		heads := map[string]bool{}
		for _, r := range rows[from:to] {
			heads[label(r)] = true
		}
		above = len(heads)+1+below+lineCount(drawn[from:to]) <= room
	}
	var lines []string
	shown := map[string]bool{}
	for i := from; i < to; i++ {
		if l := label(rows[i]); !shown[l] {
			lines = append(lines, styleLabel.UnsetWidth().Render(l))
			shown[l] = true
		}
		if i == from && above {
			lines = append(lines, "  "+styleDim.Render(fmt.Sprintf("+%d more", from)))
		}
		lines = append(lines, drawn[i]...)
	}
	if to < len(rows) {
		lines = append(lines, "  "+styleDim.Render(fmt.Sprintf("+%d more", len(rows)-to)))
	}
	return append(lines, errLines...)
}

// lineCount is how many lines the rows drawn take.
func lineCount(drawn [][]string) int {
	n := 0
	for _, d := range drawn {
		n += len(d)
	}
	return n
}

// passWindow is the rows from and up to to that are shown of those drawn,
// in room lines: the selected row, and as many around it as fit. A panel
// too short for even that row still shows it, and nothing more. The window
// moves only as far as the selection
// needs it to, so it stays put while the selection moves within it, and it
// is filled again from above when rows below it go.
func (m *Model) passWindow(drawn [][]string, room int) (int, int) {
	fits := func(from, to int) bool {
		return to-from <= 1 || lineCount(drawn[from:to]) <= room
	}
	sel := 0
	if m.passFocus {
		sel = m.passRow
	} else {
		m.passTop = 0
	}
	top := max(min(m.passTop, sel), 0)
	for !fits(top, sel+1) {
		top++
	}
	end := sel + 1
	for end < len(drawn) && fits(top, end+1) {
		end++
	}
	for top > 0 && fits(top-1, end) {
		top--
	}
	m.passTop = top
	return top, end
}

// passKey handles a key while the server panel's passthroughs have the
// focus, and reports whether it was one of theirs.
func (m *Model) passKey(s string) (tea.Cmd, bool) {
	kind, name := m.selected()
	if kind != rowServer {
		m.passFocus = false
		return nil, false
	}
	sel := m.passSel
	rows, _ := m.passRows(name)
	if !m.passFocus {
		return nil, false // the rows were all closed
	}
	// The selected row was closed since it was drawn, and the selection
	// moved to a row the user has not seen selected yet.
	moved := rows[m.passRow].key() != sel
	switch s {
	case "tab", "esc":
		m.passFocus, m.panelErr = false, ""
	case "up", "k":
		m.passRow = max(m.passRow-1, 0)
		m.passSel = rows[m.passRow].key()
	case "down", "j":
		m.passRow = min(m.passRow+1, len(rows)-1)
		m.passSel = rows[m.passRow].key()
	case "enter":
		tr := rows[m.passRow].tree
		if tr == nil {
			return nil, false
		}
		if moved {
			return nil, true // the next draw shows the tree enter would open
		}
		return m.openTree(name, *tr), true
	case "x":
		if moved {
			return nil, true // the next draw shows the row x would close
		}
		if p := rows[m.passRow].pass; p != nil && m.passClosing[p.ID] {
			return nil, true // its close is running
		}
		return m.closeRow(name, rows[m.passRow]), true
	default:
		return nil, false
	}
	return nil, true
}

// closeRow closes the selected passthrough, or asks before revoking the
// selected grant or no longer serving the selected tree.
func (m *Model) closeRow(server string, r passRow) tea.Cmd {
	m.panelErr = ""
	if g := r.grant; g != nil {
		m.form = &revokeForm{b: m.b, w: m.work, server: server, requester: g.Requester, kind: g.Kind}
		return nil
	}
	if tr := r.tree; tr != nil {
		m.form = &treeForm{b: m.b, w: m.work, server: server, id: tr.ID, root: m.treeRoot(*tr)}
		return nil
	}
	b, p, key := m.b, *r.pass, rowKey(rowServer, server)
	if m.passClosing == nil {
		m.passClosing = map[string]bool{}
	}
	m.passClosing[p.ID] = true
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
// not be. Either way, x may close it again.
func (m *Model) passClosed(msg passClosedMsg) {
	delete(m.passClosing, msg.p.ID)
	if msg.err != nil {
		m.refused(msg.key, msg.err)
		return
	}
	m.logs.noteOn(msg.key, fmt.Sprintf("Closed port %d for %s.", msg.p.Remote, requesterName(msg.p.Requester)))
}

// revokeForm asks before taking back a grant, and takes it back on yes. It is
// not a confirmForm, which edits the config file.
type revokeForm struct {
	b                       Backend
	w                       *worker
	server, requester, kind string
	busy                    bool
	err                     string
	seq                     int
}

// revokedMsg is the outcome of taking back a grant.
type revokedMsg struct {
	seq                     int
	server, requester, kind string
	err                     error
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
		seq, b, server, requester, kind := f.seq, f.b, f.server, f.requester, f.kind
		return f.w.do(func(context.Context) tea.Msg {
			return revokedMsg{seq, server, requester, kind, b.RevokeGrant(server, requester, kind)}
		})
	case "n", "esc":
		return closeForm
	}
	return nil
}

// lines draws what the question adds to its title.
func (f *revokeForm) lines(width int) []string {
	note := "Open passthroughs stay open."
	if f.kind == grants.KindDocuments {
		note = "Documents being served stay served."
	}
	lines := wrap(note, width)
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
	return heading{title: "Stop always allowing " + requesterName(f.requester) + grantWhat(f.kind) + " " + docWhere(f.server) + "?"}
}

// keys are y and n, and none while the revoke runs.
func (f *revokeForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	return []footerItem{{"y", "revoke"}, {"n", "keep"}}
}

// grantWhat is what a grant of kind lets its requester do, as words that
// follow the requester's name: " to show documents" for a documents grant,
// and nothing for a passthrough grant, the first kind there was.
func grantWhat(kind string) string {
	if kind == grants.KindDocuments {
		return " to show documents"
	}
	return ""
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
		// A read still running began before the revoke, and may hold the
		// grant: its outcome is dropped, and the grants read again at
		// once.
		e.gen++
		e.list = slices.DeleteFunc(slices.Clone(e.list), func(g grants.Grant) bool {
			return g.Requester == msg.requester && g.Kind == msg.kind
		})
		e.at, e.busy = time.Time{}, false
	}
	m.logs.noteOn(rowKey(rowServer, msg.server), fmt.Sprintf("Stopped always allowing %s%s.", requesterName(msg.requester), grantWhat(msg.kind)))
}

// agentCounts are the lines of Home's agents: how many passthroughs are
// open and trees served on each server, by name; none when there are none.
func (m *Model) agentCounts(width int) []string {
	passes, trees := map[string]int{}, map[string]int{}
	names := map[string]bool{}
	for _, p := range m.b.Passthroughs("") {
		passes[p.Server]++
		names[p.Server] = true
	}
	for _, tr := range m.b.Documents("") {
		trees[tr.Server]++
		names[tr.Server] = true
	}
	var parts []string
	for _, s := range slices.Sorted(maps.Keys(names)) {
		var counts []string
		if n := passes[s]; n > 0 {
			counts = append(counts, plural(n, "passthrough"))
		}
		if n := trees[s]; n > 0 {
			counts = append(counts, plural(n, "document"))
		}
		parts = append(parts, clean(s)+" "+strings.Join(counts, ", "))
	}
	if len(parts) == 0 {
		return nil
	}
	return field("agents", strings.Join(parts, " · "), width)
}

// grantsEvery is how long the grants read for a server are fresh.
const grantsEvery = 5 * time.Second

// grantsEntry is the grants last read for a server.
type grantsEntry struct {
	list []grants.Grant
	err  error
	at   time.Time // when they were read; zero when they must be read again
	busy bool      // a read is running
	// gen counts the revokes made on the server; a read begun before the
	// latest is stale.
	gen int
}

// grantsMsg is the outcome of reading a server's grants, a read begun at
// generation gen of its entry.
type grantsMsg struct {
	server string
	list   []grants.Grant
	err    error
	gen    int
}

// fetchGrants reads the selected server's grants on the worker when they are
// not known or no longer fresh. Reading them blocks, so the panel draws
// from what was last read.
func (m *Model) fetchGrants() tea.Cmd {
	kind, name := m.selected()
	if kind != rowServer || m.quitting {
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
	b, gen := m.b, e.gen
	return m.work.do(func(context.Context) tea.Msg {
		list, err := b.Grants(name)
		return grantsMsg{name, list, err, gen}
	})
}

// gotGrants keeps the grants that were read, unless a revoke was made
// since the read began, as the read may still hold the grant revoked.
func (m *Model) gotGrants(msg grantsMsg) {
	if m.grantCache == nil {
		m.grantCache = map[string]*grantsEntry{}
	}
	gen := 0
	if e := m.grantCache[msg.server]; e != nil {
		if msg.gen < e.gen {
			return
		}
		gen = e.gen
	}
	m.grantCache[msg.server] = &grantsEntry{list: msg.list, err: msg.err, at: m.now(), gen: gen}
}
