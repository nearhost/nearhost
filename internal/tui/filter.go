package tui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// newFilter is the sidebar's filter by name.
func newFilter() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 64
	st := in.Styles()
	st.Focused.Text = styleText
	st.Blurred.Text = styleText
	st.Cursor.Color = colourBeacon
	st.Cursor.Blink = false
	in.SetStyles(st)
	return in
}

// query is what the sidebar is filtered by; empty when it is not.
func (m *Model) query() string { return strings.TrimSpace(m.filter.Value()) }

// matches reports whether a project's name holds the query, ignoring case.
func matches(name, query string) bool {
	at, _ := matchAt(name, query)
	return at >= 0
}

// matchAt is where query first appears in name, ignoring case, and where
// it ends, counted in runes; -1 when it does not appear. Runes are compared
// one by one, so a name whose lower case is longer than it, such as one
// with İ, is never cut inside a rune.
func matchAt(name, query string) (start, end int) {
	rn, rq := []rune(name), []rune(query)
	for i := 0; i+len(rq) <= len(rn); i++ {
		found := true
		for j, q := range rq {
			if unicode.ToLower(rn[i+j]) != unicode.ToLower(q) {
				found = false
				break
			}
		}
		if found {
			return i, i + len(rq)
		}
	}
	return -1, -1
}

// filterKey handles a key while the filter is being typed.
func (m *Model) filterKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		m.filtering = false
		m.filter.Blur()
		return nil
	case "esc":
		m.clearFilter()
		return nil
	case "up", "down":
		return m.action(k.String())
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(k)
	m.keepSelectionShown()
	return cmd
}

// clearFilter stops filtering and shows every row again.
func (m *Model) clearFilter() {
	m.filtering = false
	m.filter.Blur()
	m.filter.SetValue("")
}

// keepSelectionShown selects the first row shown when the selected one has
// been filtered out.
func (m *Model) keepSelectionShown() {
	all := m.rows()
	for _, r := range all {
		if r.key() == m.sel {
			return
		}
	}
	for _, r := range all {
		if r.kind == rowProject {
			m.selectRow(r.key())
			return
		}
	}
	if len(all) > 0 {
		m.selectRow(all[0].key())
	}
}

// filterLines are the sidebar's first lines while it is filtered: what is
// typed, and how many projects match.
func (m *Model) filterLines(width int) []string {
	typed := m.filter.View()
	if !m.filtering {
		typed = styleText.Render(m.query())
	}
	shown := 0
	for _, r := range m.rows() {
		if r.kind == rowProject {
			shown++
		}
	}
	count := fmt.Sprintf("%d of %d match", shown, len(m.cfg.Projects))
	return []string{
		fit(" "+styleBeacon.Render("/")+" "+typed, width),
		fit(" "+styleDim.Render(count), width),
	}
}

// highlight draws name in style with the part matching query underlined in
// the beacon colour, as segs.
func highlight(name, query string, style seg) []seg {
	at, end := matchAt(name, query)
	if query == "" || at < 0 {
		return []seg{{style.style, name}}
	}
	rn := []rune(name)
	return []seg{
		{style.style, string(rn[:at])},
		{style.style.Foreground(colourBeacon).Underline(true), string(rn[at:end])},
		{style.style, string(rn[end:])},
	}
}
