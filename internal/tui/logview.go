package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	searching bool                 // the search is being typed
	cursor    int                  // the seq of the entry under the cursor
	open      int                  // the seq of the request opened; 0 when none is
	follow    bool                 // the cursor stays on the newest entry
	wrap      bool                 // entries that are not requests wrap
	reveal    bool                 // an opened request shows its cookies and credentials
	bodies    map[string][2]string // kept bodies as text and a note, by exchange and part
	confirm   int                  // the seq of the request waiting for y to be sent again; 0 when none is
	said      string               // what the last replay said, shown until the next key
	pausedAt  int                  // the newest seq when following stopped
	vp        viewport.Model       // draws the rows in view; its own scroll position stays at the top
	yoff      int                  // the first line in view, counted from the top of the whole log
	cache     logCache             // the filtered entries, made again only when what they come from changes
	heights   map[int]int          // the lines of each wrapping entry, by seq, at heightsAt
	heightsAt int                  // the width the entries in heights were measured at
	rows      []int                // the lines of each shown entry in the frame being drawn
}

// logCache is what the full-screen log derives from the entries of its row:
// those that are shown, and the counts the tabs and the footer show. It is
// made again when the log gains or drops an entry (the version of the logs
// goes up), or when the row, the kind or the search it was made for
// changes, so no caller can act on an entry that is no longer there.
type logCache struct {
	ok     bool
	src    *logs
	ver    int
	key    string
	kind   logKind
	search string
	shown  []logEntry
	total  int // all the entries of the row, whatever the kind and search
	errs   int // those of them that failed
	runs   int // how many times the entries were filtered; tests read it
}

func newLogView(key string) *logView {
	in := newFilter()
	return &logView{key: key, search: in, follow: true, wrap: true, vp: viewport.New()}
}

// openLog shows the full-screen log of the selected row.
func (m *Model) openLog() {
	m.showHelp = false
	m.logView = newLogView(m.sel)
}

// logData is the entries of the log view's row, filtered. It filters again
// only when the cache no longer matches the logs' version, the row, the kind
// or the search, so every caller in an update shares one pass over the log.
func (m *Model) logData() *logCache {
	v := m.logView
	search := strings.TrimSpace(v.search.Value())
	c := &v.cache
	if c.ok && c.src == m.logs && c.ver == m.logs.ver && c.key == v.key && c.kind == v.kind && c.search == search {
		return c
	}
	all := m.logs.entries(v.key, "")
	c.errs = 0
	for _, e := range all {
		if e.err {
			c.errs++
		}
	}
	// A fresh slice, as the ring reuses its own when it drops entries.
	c.shown = nil
	for _, e := range m.logs.entries(v.key, search) {
		if v.kind.shows(e) {
			c.shown = append(c.shown, e)
		}
	}
	c.ok, c.src, c.ver, c.key, c.kind, c.search = true, m.logs, m.logs.ver, v.key, v.kind, search
	c.total = len(all)
	c.runs++
	return c
}

// shown are the entries the log view lists: those of its kind that hold
// the search.
func (m *Model) shown() []logEntry { return m.logData().shown }

// entryRows is how many lines each of shown takes at width, with the open
// request's extra lines counted for entry openAt. The lines of a wrapping
// entry are counted once and remembered by seq, until the width changes: a
// request is always one line, and so is every entry when the log does not
// wrap.
func (v *logView) entryRows(shown []logEntry, width, openAt, extra int) []int {
	if v.heights == nil || v.heightsAt != width || len(v.heights) > 4*len(shown)+1024 {
		v.heights, v.heightsAt = map[int]int{}, width
	}
	v.rows = v.rows[:0]
	for i, e := range shown {
		n := 1
		if v.wrap && e.req == nil {
			h, ok := v.heights[e.seq]
			if !ok {
				h = entryHeight(e, width)
				v.heights[e.seq] = h
			}
			n = h
		}
		if i == openAt {
			n += extra
		}
		v.rows = append(v.rows, n)
	}
	return v.rows
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
	if m.showHelp && !v.searching && (s == "?" || s == "esc") {
		m.showHelp = false // the log keeps its place and its search
		return nil
	}
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
	v.said = ""
	if v.confirm != 0 {
		seq := v.confirm
		v.confirm = 0
		if s == "y" {
			for _, e := range shown {
				if e.seq == seq {
					return m.replay(e)
				}
			}
		}
		return nil // any other key keeps the request unsent
	}
	switch s {
	case "?":
		m.showHelp = true
	case "r":
		if at >= 0 && shown[at].req != nil {
			return m.askReplay(shown[at])
		}
	case "esc", "q":
		if v.search.Value() != "" && s == "esc" {
			v.search.SetValue("")
			return nil
		}
		m.logView, m.showHelp = nil, false
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
			a := shown[at].req
			line := "Copied " + a.Method + " " + clean(a.URI) + " as curl."
			var command string
			var err error
			if ex, kept := m.b.Captured(a.ID); kept && a.ID != "" {
				command, err = curlForExchange(ex)
				if why := bodyLeftOut(ex); why != "" {
					line = strings.TrimSuffix(line, ".") + ", without its body: " + why + "."
				}
			} else {
				command, err = curlFor(*a)
			}
			if err != nil {
				return m.alertProblem(v.key, "This request cannot be copied as curl.")
			}
			return tea.Batch(copyText(v.key, command), m.alert(v.key, line))
		}
	case "v":
		v.reveal = !v.reveal
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
	// The lines of each entry come from heights worked out once per width, so
	// the whole log is measured but only the entries in view are drawn.
	openAt, extra := -1, []string(nil)
	for i, e := range shown {
		if e.req != nil && e.seq == v.open {
			openAt, extra = i, m.requestLines(e, width)
			break
		}
	}
	rows := v.entryRows(shown, width-1, openAt, len(extra))
	total, cursorLine := 0, 0
	for i, n := range rows {
		if i == at {
			cursorLine = total
		}
		total += n
	}
	foot := 1
	if m.showHelp {
		foot = logHelpRows(m.width)
	}
	height := max(m.height-5-foot, 1) // the header and its rule, the tabs and a blank line, the rule and the footer
	newer := 0
	if !v.follow {
		newer = len(shown) - sort.Search(len(shown), func(i int) bool { return shown[i].seq > v.pausedAt })
	}
	if newer > 0 || v.confirm != 0 || v.said != "" {
		height--
	}
	// The first line in view: the log stays where it was unless the cursor
	// left the view, and never passes the last page.
	last := max(0, total-height)
	v.yoff = min(v.yoff, last)
	if v.follow {
		v.yoff = last
	} else if cursorLine < v.yoff || cursorLine >= v.yoff+height {
		v.yoff = min(max(cursorLine, 0), last)
	}
	var window []string
	start := 0
	for i, e := range shown {
		if start >= v.yoff+height {
			break
		}
		end := start + rows[i]
		if end <= v.yoff {
			start = end
			continue
		}
		var block []string
		el := entryLines(e, width-1, sources)
		if !v.wrap {
			el = el[:1]
		}
		for j, l := range el {
			lead := " "
			if i == at && j == 0 {
				lead = styleBeacon.Render("▌")
			}
			block = append(block, lead+l)
		}
		if i == openAt {
			block = append(block, extra...)
		}
		for j, l := range block {
			if n := start + j; n >= v.yoff && n < v.yoff+height {
				window = append(window, l)
			}
		}
		start = end
	}
	v.vp.SetWidth(width)
	v.vp.SetHeight(height)
	v.vp.SetContentLines(window)
	v.vp.SetYOffset(0)
	body := strings.Split(v.vp.View(), "\n")
	bar := scrollbar(v.yoff, total, height)
	out := []string{m.bar(m.logCrumbs(), m.logState()), rule(m.width), m.bar(m.logTabs(), m.logCount(len(shown))), strings.Repeat(" ", m.width)}
	for i := 0; i < height; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		out = append(out, " "+fit(l, width)+bar[i]+" ")
	}
	switch {
	case v.confirm != 0:
		out = append(out, m.bar(styleAmber.Render(m.confirmText()),
			styleBeacon.Render("y")+" "+styleDim.Render("send · any other key keeps it")))
	case v.said != "":
		out = append(out, m.bar(v.said, ""))
	case newer > 0:
		out = append(out, m.bar(styleAmber.Render(fmt.Sprintf("▼ %d new lines below", newer)),
			styleBeacon.Render("G")+" "+styleDim.Render("jump to the end and follow")))
	}
	out = append(out, rule(m.width))
	if m.showHelp {
		return strings.Join(append(out, m.logHelp()), "\n")
	}
	right := styleDim.Render(m.logName())
	m.help.SetWidth(m.width - 3 - ansi.StringWidth(m.logName()))
	return strings.Join(append(out, m.bar(m.help.ShortHelpView(bindings(logFooterItems())), right)), "\n")
}

// logKey is one key of the full-screen log: the group the full help lists
// it under, the key and what it does.
type logKey struct{ group, key, label string }

// logViewKeys are every key of the full-screen log, the most useful first.
// The footer shows as many as fit, in this order, and ? lists them all, by
// group.
var logViewKeys = []logKey{
	{"move", "j/k", "scroll"}, {"view", "enter", "expand"}, {"view", "tab", "kind"}, {"search", "/", "search"},
	{"log", "?", "keys"}, {"request", "c", "copy curl"}, {"request", "r", "replay"}, {"log", "esc", "back"},
	{"move", "G", "follow"}, {"view", "w", "wrap"}, {"view", "v", "reveal cookies"}, {"move", "g", "top"},
	{"move", "pgup/pgdn", "page"}, {"view", "shift+tab", "kind back"}, {"log", "q", "back"},
}

// logFooterItems are the log's keys as footer items.
func logFooterItems() []footerItem {
	items := make([]footerItem, len(logViewKeys))
	for i, k := range logViewKeys {
		items[i] = footerItem{k.key, k.label}
	}
	return items
}

// logHelpGroups are the log's keys by group, in the order the groups first
// appear in logViewKeys, with ? as the key that closes the help.
func logHelpGroups() []helpGroup {
	var groups []helpGroup
	for _, k := range logViewKeys {
		label := k.label
		if k.key == "?" {
			label = "close this list"
		}
		at := -1
		for i, g := range groups {
			if g.title == k.group {
				at = i
			}
		}
		if at < 0 {
			groups = append(groups, helpGroup{title: k.group})
			at = len(groups) - 1
		}
		groups[at].keys = append(groups[at].keys, bindings([]footerItem{{k.key, label}})...)
	}
	return groups
}

// logHelpRows is how many rows the log's full help takes in a window of
// width.
func logHelpRows(width int) int { return helpRowsOf(logHelpGroups(), width) }

// logHelp draws every key of the full-screen log in place of its footer.
func (m *Model) logHelp() string { return m.helpOf(logHelpGroups()) }

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
	status := m.b.Status(name)
	url := "https://" + clean(p.Alias)
	return m.projectDot(status).String() + " " + string(status.State) + styleDim.Render(" · ") + styleBeacon.Underline(true).Hyperlink(url).Render(url)
}

// logTabs are the kinds to choose from, and the search.
func (m *Model) logTabs() string {
	v := m.logView
	var parts []string
	for i, name := range kindNames {
		text := " " + name + " "
		if logKind(i) == kindErrors {
			if errs := m.logData().errs; errs > 0 {
				text = " " + name + " " + strconv.Itoa(errs) + " "
			}
		}
		if logKind(i) == v.kind {
			if m.monochrome {
				text = "[" + strings.TrimSuffix(strings.TrimPrefix(text, " "), " ") + "]"
			}
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
	total := m.logData().total
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

// secretHeaders are the request headers whose values are hidden until v
// reveals them. The access log redacts them; a kept exchange has them.
var secretHeaders = map[string]bool{"cookie": true, "authorization": true, "proxy-authorization": true}

// bodyRows is how many lines of a body an opened request shows.
const bodyRows = 8

// requestLines are an opened request: what was asked, what came back, the
// route it took, and what can be done with it. With the exchange kept, the
// headers are the real ones and the bodies are shown.
func (m *Model) requestLines(e logEntry, width int) []string {
	a := e.req
	var ex proxy.Exchange
	kept := false
	if a.ID != "" {
		ex, kept = m.b.Captured(a.ID)
	}
	bar := "   " + styleDim.Render("│") + " "
	label := func(s string) string { return styleDim.Render(fit(s, 9)) }
	under := bar + strings.Repeat(" ", 9)
	headers := http.Header(a.Headers)
	if kept {
		headers = ex.ReqHeader
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var heads []string
	secrets := false
	for _, name := range names {
		value := strings.Join(headers[name], ", ")
		lower := strings.ToLower(name)
		if kept && secretHeaders[lower] {
			secrets = true
			if !m.logView.reveal {
				value = "•••"
			}
		}
		heads = append(heads, lower+": "+clean(value))
	}
	req := "host: " + clean(a.Host)
	if len(heads) > 0 {
		req += styleDim.Render(" · ") + strings.Join(heads, styleDim.Render(" · "))
	}
	// The headers wrap, so none is cut off; cookies are worth seeing whole.
	var lines []string
	for i, l := range wrap(req, width-ansi.StringWidth(under)) {
		lead := under
		if i == 0 {
			lead = bar + label("request")
		}
		lines = append(lines, lead+l)
	}
	if kept && (ex.ReqBodySize > 0 || ex.ReqBodyShort) {
		text, note := m.logView.decoded(ex.ID+" sent", ex.ReqBody, ex.ReqHeader, ex.ReqBodyCut())
		if short := shortBodyNote(ex); short != "" {
			note = strings.TrimPrefix(note+" · "+short, " · ")
		}
		lines = append(lines, bodyLines(bar+label("sent"), under, text, note, width)...)
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
	lines = append(lines, bar+label("response")+resp)
	switch {
	case kept && ex.Err != "":
		lines = append(lines, bar+label("why")+styleRed.Render(clean(ex.Err)))
	case kept && ex.Upgraded:
		lines = append(lines, under+styleDim.Render("upgraded; what passes over the connection is not kept"))
	case kept:
		text, note := m.logView.decoded(ex.ID+" body", ex.RespBody, ex.RespHeader, ex.RespBodyCut())
		lines = append(lines, bodyLines(bar+label("body"), under, text, note, width)...)
	case a.ID != "":
		lines = append(lines, under+styleDim.Render("the bodies are no longer kept"))
	}
	if route := m.routeOf(e.source); route != "" {
		lines = append(lines, bar+label("route")+route)
	}
	keys := styleBeacon.Render("c") + styleDim.Render(" copy as curl · ")
	if kept && proxy.CanReplay(ex) == nil {
		keys += styleBeacon.Render("r") + styleDim.Render(" replay · ")
	}
	if secrets {
		verb := " reveal · "
		if m.logView.reveal {
			verb = " hide · "
		}
		keys += styleBeacon.Render("v") + styleDim.Render(verb)
	}
	lines = append(lines, bar+keys+styleBeacon.Render("enter")+styleDim.Render(" collapse"))
	for i, l := range lines {
		lines[i] = fit(l, width)
	}
	return lines
}

// bodyText reads a kept body as text; tests count its calls.
var bodyText = proxy.BodyText

// decoded is a kept body as text, read once for key and remembered while
// the log is open: decoding and indenting on every frame would be wasted.
func (v *logView) decoded(key string, body []byte, header http.Header, cut bool) (text, note string) {
	if d, ok := v.bodies[key]; ok {
		return d[0], d[1]
	}
	text, note = bodyText(body, header, cut)
	if v.bodies == nil {
		v.bodies = map[string][2]string{}
	}
	v.bodies[key] = [2]string{text, note}
	return text, note
}

// bodyLines draw a kept body: its first bodyRows lines, the first after
// first and the rest after rest, and what was left out.
func bodyLines(first, rest, text, note string, width int) []string {
	var rows []string
	if text != "" {
		rows = strings.Split(strings.TrimRight(text, "\n"), "\n")
	}
	more := 0
	if len(rows) > bodyRows {
		more, rows = len(rows)-bodyRows, rows[:bodyRows]
	}
	var out []string
	lead := first
	for _, r := range rows {
		out = append(out, fit(lead+clean(r), width))
		lead = rest
	}
	var tail []string
	if more > 0 {
		tail = append(tail, fmt.Sprintf("%d more lines", more))
	}
	if note != "" {
		tail = append(tail, note)
	}
	if len(tail) > 0 {
		out = append(out, fit(lead+styleDim.Render(strings.Join(tail, " · ")), width))
	}
	if len(out) == 0 {
		out = append(out, fit(first+styleDim.Render("empty"), width))
	}
	return out
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

// shortBodyNote says how much of a request body reached the app, when not
// all of it did, or is empty.
func shortBodyNote(ex proxy.Exchange) string {
	switch {
	case !ex.ReqBodyShort || ex.Upgraded:
		return ""
	case ex.ReqBodyNeverReached() && ex.ReqBodyLength >= 0:
		return "(none of " + sizeText(int(ex.ReqBodyLength)) + " reached the app)"
	case ex.ReqBodyNeverReached():
		return "(none of the body reached the app)"
	}
	n := sizeText(int(ex.ReqBodySize))
	if n == "" {
		n = "0 B"
	}
	who := "app read "
	if ex.ReqBodyAborted {
		who = "client sent "
	}
	switch {
	case ex.ReqBodyLength >= 0 && ex.ReqBodyAborted:
		return "(" + who + n + " of " + sizeText(int(ex.ReqBodyLength)) + ", then stopped)"
	case ex.ReqBodyLength >= 0:
		return "(" + who + n + " of " + sizeText(int(ex.ReqBodyLength)) + ")"
	}
	return "(" + who + n + ", then stopped)"
}

// bodyLeftOut says why a curl command for ex has no body when it had one,
// or is empty.
func bodyLeftOut(ex proxy.Exchange) string {
	switch {
	case ex.Upgraded:
		return ""
	case ex.ReqBodyAborted:
		return "the client stopped sending it"
	case ex.ReqBodyNeverReached():
		return "it never reached the app"
	case ex.ReqBodyShort:
		return "the app stopped reading it"
	case ex.ReqBodyCut():
		return "only its start was kept"
	}
	return ""
}

// skipInCurl are the headers a curl command leaves out: the URL carries
// the host, and curl works out the rest itself.
var skipInCurl = map[string]bool{
	"host": true, "content-length": true, "connection": true, "keep-alive": true,
	"transfer-encoding": true, "te": true, "upgrade": true,
}

// curlForExchange is a curl command that makes a kept request again, with
// its real headers and, when it was kept whole, its body. It fails for a
// request that holds a control character.
func curlForExchange(ex proxy.Exchange) (string, error) {
	names := make([]string, 0, len(ex.ReqHeader))
	for name := range ex.ReqHeader {
		names = append(names, name)
	}
	sort.Strings(names)
	var headers [][2]string
	for _, name := range names {
		if skipInCurl[strings.ToLower(name)] {
			continue
		}
		for _, value := range ex.ReqHeader[name] {
			headers = append(headers, [2]string{name, value})
		}
	}
	leftOut := ex.ReqBodyCut() || ex.ReqBodyShort
	return curlCommand(runtime.GOOS, ex.Method, "https://"+ex.Host+ex.URI, headers, ex.ReqBody, leftOut)
}

// curlFor is a curl command that makes the request again. Headers whose
// values Caddy redacted are left out, as is Host, which the URL carries. It
// fails for a request that holds a control character.
func curlFor(a proxy.Access) (string, error) {
	names := make([]string, 0, len(a.Headers))
	for name := range a.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var headers [][2]string
	for _, name := range names {
		if strings.EqualFold(name, "Host") {
			continue
		}
		for _, value := range a.Headers[name] {
			if value != "REDACTED" {
				headers = append(headers, [2]string{name, value})
			}
		}
	}
	return curlCommand(runtime.GOOS, a.Method, "https://"+a.Host+a.URI, headers, nil, false)
}

// scrollbar is a column height cells tall, with a thumb where the view is
// in total lines from offset.
func scrollbar(offset, total, height int) []string {
	return scrollbarOf(offset, height, total, height)
}

// scrollbarOf is a column height cells tall, with a thumb for a view of
// view items from offset among total: blank cells, and the thumb, so a
// caller can draw its own track. With everything in view there is no
// thumb.
func scrollbarOf(offset, view, total, height int) []string {
	out := make([]string, height)
	for i := range out {
		out[i] = " "
	}
	if total <= view || height <= 0 {
		return out
	}
	size := min(max(height*view/total, 1), height)
	// The thumb travels the track left by its size, so the last view puts
	// it at the bottom.
	top := min(max(offset, 0)*(height-size)/(total-view), height-size)
	for i := top; i < top+size; i++ {
		out[i] = styleBox.Render(thumb)
	}
	return out
}

// thumb is the part of a scrollbar that shows where the view is.
const thumb = "┃"

// safeMethods are sent again without asking: they change nothing.
var safeMethods = map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true}

// replayedMsg is the outcome of sending a request again.
type replayedMsg struct {
	method, uri string
	status      int
	err         error
}

// askReplay sends a request again at once when it is safe, asks first when
// it could change something, and says why when it cannot be sent.
func (m *Model) askReplay(e logEntry) tea.Cmd {
	v := m.logView
	ex, kept := m.b.Captured(e.req.ID)
	switch {
	case e.req.ID == "" || !kept:
		v.said = styleAmber.Render("That request is no longer kept, so it cannot be sent again.")
		return nil
	case proxy.CanReplay(ex) != nil:
		why := proxy.CanReplay(ex)
		if errors.Is(why, proxy.ErrAppStoppedReading) || errors.Is(why, proxy.ErrClientStoppedSending) || errors.Is(why, proxy.ErrBodyNeverReached) {
			v.said = styleAmber.Render(sentence(why.Error()) + ".") // already says it cannot be sent
		} else {
			v.said = styleAmber.Render(sentence(why.Error()) + ", so it cannot be sent again.")
		}
		return nil
	case !safeMethods[ex.Method]:
		v.confirm = e.seq
		return nil
	}
	return m.replay(e)
}

// confirmText asks whether to send the request waiting for y again.
func (m *Model) confirmText() string {
	for _, e := range m.shown() {
		if e.seq == m.logView.confirm {
			return "Send " + e.req.Method + " " + clean(e.req.URI) + " again? It may change something."
		}
	}
	return ""
}

// replay sends a request again in the background.
func (m *Model) replay(e logEntry) tea.Cmd {
	b, id, method, uri := m.b, e.req.ID, e.req.Method, e.req.URI
	return m.work.do(func(ctx context.Context) tea.Msg {
		status, err := b.Replay(ctx, id)
		return replayedMsg{method, uri, status, err}
	})
}

// replayed says how sending a request again went.
func (m *Model) replayed(msg replayedMsg) {
	text := fmt.Sprintf("Sent %s %s again: %d.", clean(msg.method), clean(msg.uri), msg.status)
	if msg.err != nil {
		text = fmt.Sprintf("Could not send %s %s again: %s.", clean(msg.method), clean(msg.uri), strings.TrimRight(clean(msg.err.Error()), "."))
	}
	if m.logView != nil {
		m.logView.said = text
		m.logs.noteOn(m.logView.key, text)
		return
	}
	m.logs.note(text)
}
