package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
)

// logKind is which entries the full-screen log shows.
type logKind int

const (
	kindAll logKind = iota
	kindRequests
	kindEvents
	kindErrors
)

var kindNames = []string{"all", "requests", "events", "errors"}

// shows reports whether an entry of a log is of kind k.
func (k logKind) shows(e logEntry) bool {
	switch k {
	case kindRequests:
		return e.req != nil
	case kindEvents:
		return e.req == nil
	case kindErrors:
		return e.err
	}
	return true
}

// logView is the full-screen log of a row: its entries of one kind,
// searched, with a cursor, and the request under it opened.
type logView struct {
	key       string // the row whose log this is
	kind      logKind
	search    textinput.Model
	searching bool // the search is being typed
	cursor    int  // the seq of the entry under the cursor
	open      int  // the seq of the request opened; 0 when none is
	follow    bool // the cursor stays on the newest entry
	wrap      bool // entries that are not requests wrap
	pausedAt  int  // the newest seq when following stopped
	vp        viewport.Model
}

func newLogView(key string) *logView {
	in := newFilter()
	return &logView{key: key, search: in, follow: true, wrap: true, vp: viewport.New()}
}

// openLog shows the full-screen log of the selected row.
func (m *Model) openLog() {
	m.logView = newLogView(m.sel)
}

// shown are the entries the log view lists: those of its kind that hold
// the search.
func (m *Model) shown() []logEntry {
	v := m.logView
	var out []logEntry
	for _, e := range m.logs.entries(v.key, strings.TrimSpace(v.search.Value())) {
		if v.kind.shows(e) {
			out = append(out, e)
		}
	}
	return out
}

// at is the index of the entry under the cursor in shown, the newest when
// following or when it is not there.
func (v *logView) at(shown []logEntry) int {
	if !v.follow {
		for i, e := range shown {
			if e.seq == v.cursor {
				return i
			}
		}
	}
	return len(shown) - 1
}

// moveTo puts the cursor on entry i of shown; the newest entry follows.
func (v *logView) moveTo(shown []logEntry, i int) {
	if len(shown) == 0 {
		return
	}
	i = min(max(i, 0), len(shown)-1)
	newest := shown[len(shown)-1].seq
	if i == len(shown)-1 {
		v.follow = true
		return
	}
	if v.follow {
		v.pausedAt = newest
	}
	v.follow, v.cursor = false, shown[i].seq
}

// logViewKey handles a key in the full-screen log.
func (m *Model) logViewKey(k tea.KeyPressMsg) tea.Cmd {
	v := m.logView
	s := k.String()
	if v.searching {
		switch s {
		case "enter":
			v.searching = false
			v.search.Blur()
		case "esc":
			v.searching = false
			v.search.Blur()
			v.search.SetValue("")
		default:
			var cmd tea.Cmd
			v.search, cmd = v.search.Update(k)
			return cmd
		}
		return nil
	}
	shown := m.shown()
	at := v.at(shown)
	switch s {
	case "esc", "q":
		if v.search.Value() != "" && s == "esc" {
			v.search.SetValue("")
			return nil
		}
		m.logView = nil
	case "/":
		v.searching = true
		return v.search.Focus()
	case "j", "down":
		v.moveTo(shown, at+1)
	case "k", "up":
		v.moveTo(shown, at-1)
	case "pgdown", "space":
		v.moveTo(shown, at+max(v.vp.Height()-1, 1))
	case "pgup":
		v.moveTo(shown, at-max(v.vp.Height()-1, 1))
	case "g", "home":
		v.moveTo(shown, 0)
	case "G", "end":
		v.follow = true
	case "tab":
		v.kind = (v.kind + 1) % logKind(len(kindNames))
	case "shift+tab":
		v.kind = (v.kind + logKind(len(kindNames)) - 1) % logKind(len(kindNames))
	case "w":
		v.wrap = !v.wrap
	case "enter":
		if at >= 0 && shown[at].req != nil {
			if v.open == shown[at].seq {
				v.open = 0
			} else {
				v.open = shown[at].seq
			}
		}
	case "c":
		if at >= 0 && shown[at].req != nil {
			m.logs.noteOn(v.key, "Copied "+shown[at].req.Method+" "+clean(shown[at].req.URI)+" as curl.")
			return tea.SetClipboard(curlFor(*shown[at].req))
		}
	}
	return nil
}

// logWheel scrolls the full-screen log.
func (m *Model) logWheel(mouse tea.Mouse) {
	shown := m.shown()
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.logView.moveTo(shown, m.logView.at(shown)-3)
	case tea.MouseWheelDown:
		m.logView.moveTo(shown, m.logView.at(shown)+3)
	}
}

// logScreen draws the full-screen log.
func (m *Model) logScreen() string {
	v := m.logView
	width := m.width - 3 // a cell of space at the left, the scrollbar and a cell at the right
	shown := m.shown()
	at := v.at(shown)
	sources := !strings.HasPrefix(v.key, "project:")
	var lines []string
	cursorLine := 0
	for i, e := range shown {
		if i == at {
			cursorLine = len(lines)
		}
		el := entryLines(e, width-1, sources)
		if !v.wrap {
			el = el[:1]
		}
		for j, l := range el {
			lead := " "
			if i == at && j == 0 {
				lead = styleBeacon.Render("▌")
			}
			lines = append(lines, lead+l)
		}
		if e.req != nil && e.seq == v.open {
			lines = append(lines, m.requestLines(e, width)...)
		}
	}
	height := m.height - 6 // the header and its rule, the tabs and a blank line, the rule and the footer
	newer := 0
	if !v.follow {
		for _, e := range shown {
			if e.seq > v.pausedAt {
				newer++
			}
		}
	}
	if newer > 0 {
		height--
	}
	v.vp.SetWidth(width)
	v.vp.SetHeight(height)
	v.vp.SetContentLines(lines)
	if v.follow {
		v.vp.GotoBottom()
	} else {
		v.vp.EnsureVisible(cursorLine, 0, 0)
	}
	body := strings.Split(v.vp.View(), "\n")
	bar := scrollbar(v.vp.YOffset(), len(lines), height)
	out := []string{m.bar(m.logCrumbs(), m.logState()), rule(m.width), m.bar(m.logTabs(), m.logCount(len(shown))), strings.Repeat(" ", m.width)}
	for i := 0; i < height; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		out = append(out, " "+fit(l, width)+bar[i]+" ")
	}
	if newer > 0 {
		out = append(out, m.bar(styleAmber.Render(fmt.Sprintf("▼ %d new lines below", newer)),
			styleBeacon.Render("G")+" "+styleDim.Render("jump to the end and follow")))
	}
	return strings.Join(append(out, rule(m.width), m.bar(m.help.ShortHelpView(bindings(logViewKeys)), styleDim.Render(m.logName()))), "\n")
}

// logViewKeys are the full-screen log's keys.
var logViewKeys = []footerItem{
	{"j/k", "scroll"}, {"enter", "expand"}, {"tab", "kind"}, {"/", "search"},
	{"G", "follow"}, {"w", "wrap"}, {"c", "copy curl"}, {"esc", "back"},
}

// logName names the row whose log is shown.
func (m *Model) logName() string {
	switch {
	case m.logView.key == homeKey:
		return "home"
	case strings.HasPrefix(m.logView.key, "project:"):
		return clean(strings.TrimPrefix(m.logView.key, "project:"))
	}
	return clean(strings.TrimPrefix(m.logView.key, "server:"))
}

// logCrumbs is where the log is: the mark and name, the row, and log.
func (m *Model) logCrumbs() string {
	sep := styleDim.Render(" › ")
	crumbs := styleMark.Render("∩") + " " + styleTitle.Render("nearhost")
	if m.logView.key != homeKey {
		crumbs += sep + m.logName()
	}
	return crumbs + sep + "log"
}

// logState is the header's right: the project's state and link, or the
// machine's state.
func (m *Model) logState() string {
	name, ok := strings.CutPrefix(m.logView.key, "project:")
	p, found := m.cfg.Project(name)
	if !ok || !found {
		return m.machineSummary()
	}
	st := m.b.Status(name).State
	url := "https://" + clean(p.Alias)
	return m.stateDot(st).String() + " " + string(st) + styleDim.Render(" · ") + styleBeacon.Underline(true).Hyperlink(url).Render(url)
}

// logTabs are the kinds to choose from, and the search.
func (m *Model) logTabs() string {
	v := m.logView
	var parts []string
	for i, name := range kindNames {
		text := " " + name + " "
		if logKind(i) == kindErrors {
			errs := 0
			for _, e := range m.logs.entries(v.key, "") {
				if e.err {
					errs++
				}
			}
			if errs > 0 {
				text = " " + name + " " + strconv.Itoa(errs) + " "
			}
		}
		if logKind(i) == v.kind {
			parts = append(parts, styleSelected.Render(text))
		} else {
			parts = append(parts, styleDim.Render(text))
		}
	}
	tabs := strings.Join(parts, " ")
	if v.searching || v.search.Value() != "" {
		typed := v.search.View()
		if !v.searching {
			typed = v.search.Value()
		}
		tabs += "    " + styleBeacon.Render("/") + " " + typed
	}
	return tabs
}

// logCount says how many entries match, and whether the log follows.
func (m *Model) logCount(shown int) string {
	v := m.logView
	total := len(m.logs.entries(v.key, ""))
	count := fmt.Sprintf("%d lines", total)
	if v.search.Value() != "" || v.kind != kindAll {
		count = fmt.Sprintf("%d of %d lines", shown, total)
	}
	state := "following"
	if !v.follow {
		state = "paused"
	}
	return styleDim.Render(count + " · " + state)
}

// requestLines are an opened request: what was asked, what came back, the
// route it took, and what can be done with it.
func (m *Model) requestLines(e logEntry, width int) []string {
	a := e.req
	bar := "   " + styleDim.Render("│") + " "
	label := func(s string) string { return styleDim.Render(fit(s, 9)) }
	var heads []string
	names := make([]string, 0, len(a.Headers))
	for name := range a.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		heads = append(heads, strings.ToLower(clean(name))+": "+clean(strings.Join(a.Headers[name], ", ")))
	}
	req := "host: " + clean(a.Host)
	if len(heads) > 0 {
		req += styleDim.Render(" · ") + strings.Join(heads, styleDim.Render(" · "))
	}
	resp := statusText(a.Status)
	for _, part := range []string{clean(a.ContentType), sizeText(a.Size)} {
		if part != "" {
			resp += styleDim.Render(" · ") + part
		}
	}
	if a.Location != "" {
		resp += styleDim.Render(" → ") + clean(a.Location)
	}
	lines := []string{bar + label("request") + req, bar + label("response") + resp}
	if route := m.routeOf(e.source); route != "" {
		lines = append(lines, bar+label("route")+route)
	}
	lines = append(lines, bar+styleBeacon.Render("c")+styleDim.Render(" copy as curl · ")+styleBeacon.Render("enter")+styleDim.Render(" collapse"))
	for i, l := range lines {
		lines[i] = fit(l, width)
	}
	return lines
}

// routeOf is the way a project's requests take: its web route, the local
// port the proxy sends them to, and its server.
func (m *Model) routeOf(name string) string {
	p, ok := m.cfg.Project(name)
	if !ok {
		return ""
	}
	r, ok := p.WebRoute()
	if !ok {
		return ""
	}
	st := m.b.Status(name)
	arrow := styleDim.Render(" → ")
	route := strconv.Itoa(r.Port) + " web"
	if st.WebLocalPort != 0 {
		route += arrow + "127.0.0.1:" + strconv.Itoa(st.WebLocalPort)
	}
	if p.Server != config.LocalServer {
		route += arrow + clean(p.Server) + ":" + strconv.Itoa(r.Port) + styleDim.Render(" over ssh")
	}
	return route
}

// sizeText is a size in bytes in a few cells, or "" for none.
func sizeText(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f kB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
}

// curlFor is a curl command that makes the request again. Headers whose
// values Caddy redacted are left out, as is Host, which the URL carries.
func curlFor(a proxy.Access) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	parts := []string{"curl", "-i"}
	if a.Method != "" && a.Method != "GET" {
		parts = append(parts, "-X", a.Method)
	}
	parts = append(parts, quote("https://"+a.Host+a.URI))
	names := make([]string, 0, len(a.Headers))
	for name := range a.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.EqualFold(name, "Host") {
			continue
		}
		for _, value := range a.Headers[name] {
			if value != "REDACTED" {
				parts = append(parts, "-H", quote(name+": "+value))
			}
		}
	}
	return strings.Join(parts, " ")
}

// scrollbar is a column height cells tall, with a thumb where the view is
// in total lines from offset.
func scrollbar(offset, total, height int) []string {
	out := make([]string, height)
	for i := range out {
		out[i] = " "
	}
	if total <= height || height <= 0 {
		return out
	}
	size := max(height*height/total, 1)
	top := min(offset*height/total, height-size)
	for i := top; i < top+size; i++ {
		out[i] = styleBox.Render("┃")
	}
	return out
}

var styleBox = styleDim.Foreground(colourBoxEdge)
