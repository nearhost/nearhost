package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
)

// passURL is the address on this machine of an open passthrough, which is
// what the user opens.
func passURL(p engine.Passthrough) string {
	return fmt.Sprintf("http://localhost:%d", p.Local)
}

// agentRowLines draws a row of the passthroughs or documents panel as two
// lines. A passthrough has the address on this machine and the label, then,
// dim, the server and its port, the requester, the first 8 characters of the
// session when another row from the same requester on the same server needs
// telling apart, and the age. A tree has its line as treeText draws it, then,
// dim, the server it is on, the requester and the age.
func (m *Model) agentRowLines(r passRow, width int) []string {
	return m.agentRow(r, width, false)
}

// agentRow is agentRowLines, with the row filled and led by the beacon when
// sel is set.
func (m *Model) agentRow(r passRow, width int, sel bool) []string {
	var first string
	var parts []string
	switch p, tr := r.pass, r.tree; {
	case p != nil:
		first = passURL(*p)
		if l := clean(p.Label); l != "" {
			first += "   " + l
		}
		parts = []string{fmt.Sprintf("from %s, port %d", clean(p.Server), p.Remote), RequesterName(p.Requester)}
		if s := []rune(clean(p.Session)); len(s) > 0 && m.sessionAmbiguous(*p) {
			parts = append(parts, string(s[:min(len(s), 8)]))
		}
		parts = append(parts, age(m.now(), p.Opened))
	case tr != nil:
		first = m.treeText(*tr)
		parts = []string{"from " + clean(tr.Server), RequesterName(tr.Requester), age(m.now(), tr.Opened)}
	default:
		return nil
	}
	lead, text, dim := "  ", styleText, styleDim
	if sel {
		lead = styleBeacon.Render("▌") + " "
		text, dim = text.Background(colourSelected), dim.Background(colourSelected)
	}
	return []string{
		lead + text.Render(ansi.Truncate(first, width-2, "…")),
		"  " + dim.Render("  "+ansi.Truncate(strings.Join(parts, " · "), width-4, "…")),
	}
}

// sessionAmbiguous says whether another open passthrough has the same
// requester and server as p, so that only its session tells them apart.
func (m *Model) sessionAmbiguous(p engine.Passthrough) bool {
	return slices.ContainsFunc(m.b.Passthroughs(p.Server), func(o engine.Passthrough) bool {
		return o.ID != p.ID && o.Requester == p.Requester
	})
}

// agentsPanel draws the main panel of the passthroughs or documents row: its
// title, with how many are open, then what agents have opened across every
// server, or what would open something when nothing is.
func (m *Model) agentsPanel(kind rowKind, width, height int) []string {
	title, empty := "passthroughs", "No ports are open for agents. When an agent asks to open one, Nearhost asks you first."
	if kind == rowDocs {
		title, empty = "documents", "No documents are being served. When an agent writes a plan or report for you, it can ask to open it here."
	}
	rows, _ := m.rowsOf(kind, "")
	rows, _ = m.follow(rows, nil)
	head := styleTitle.Render(title)
	if kind == rowPasses && len(rows) > 0 {
		head = spread(head, styleDim.Render(fmt.Sprintf("%d open", len(rows))), width)
	}
	lines := []string{head, ""}
	if len(rows) == 0 {
		lines = append(lines, wrap(empty, width)...)
		return m.withPanelErr(lines, width)
	}
	drawn := make([][]string, len(rows))
	for i, r := range rows {
		// A blank line follows each row, the last row's too, dropped below.
		drawn[i] = append(m.agentRow(r, width, m.passFocus && i == m.passRow), "")
	}
	room := height - len(lines)
	from, to := 0, len(rows)
	if lineCount(drawn)-1 > room {
		from, to = m.passWindow(drawn, room-2)
	}
	if from > 0 {
		lines = append(lines, "  "+styleDim.Render(fmt.Sprintf("+%d more", from)))
	}
	for i := from; i < to; i++ {
		lines = append(lines, drawn[i]...)
	}
	if to < len(rows) {
		lines = append(lines, "  "+styleDim.Render(fmt.Sprintf("+%d more", len(rows)-to)))
	} else {
		lines = lines[:len(lines)-1]
	}
	return m.withPanelErr(lines, width)
}

// withPanelErr adds why the last change made in the panel was refused, when
// one was.
func (m *Model) withPanelErr(lines []string, width int) []string {
	if m.panelErr != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	return lines
}
