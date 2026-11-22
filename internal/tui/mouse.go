package tui

import (
	"image/color"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/nearhost/nearhost/internal/desktop"
)

// The TUI takes the mouse, for clicks on rows and keys and the wheel on the
// log, so the terminal no longer selects text or opens links itself. It
// does both here instead: a drag selects and copies, a double-click selects
// a word, and a click on an address opens it in the browser.

// point is a cell on the screen.
type point struct{ x, y int }

// before reports whether p comes before q in reading order.
func (p point) before(q point) bool {
	return p.y < q.y || p.y == q.y && p.x < q.x
}

// mouseState is what the left button is doing: a press not yet released,
// the text selected, and the last click, for a double-click.
type mouseState struct {
	down     bool
	from, to point // where the press was, and where the pointer is
	dragged  bool
	// span is the columns a selection keeps to, [lo, hi): the panel or box
	// the press was in, so text beside it is left out.
	lo, hi int
	// shown is whether from and to are a selection to highlight, while
	// dragging. It goes once the text is copied: the note saying so, or
	// any new log line, moves the text under it.
	shown     bool
	lastClick point
	lastAt    time.Time
	// pending is the address in log text the last click was on, with the
	// row it is on, its columns [lo, hi) and the row's text, and pendingAt
	// when: a second click on the same place within doubleClick opens it.
	pending    string
	pendingRow string
	pendingY   int
	pendingLo  int
	pendingHi  int
	pendingAt  time.Time
}

// doubleClick is the longest time between the clicks of a double-click.
const doubleClick = 400 * time.Millisecond

// edge is the vertical line between panels and on the sides of a box.
const edge = "│"

// isEdge reports whether a cell is an edge, or a scrollbar's thumb drawn
// on one: the glyph in the colour of rules, of boxes' edges or of advice's
// edges, so the same glyph in a log line is text.
func isEdge(c *uv.Cell) bool {
	if c.Content != edge && c.Content != thumb {
		return false
	}
	for _, colour := range []color.Color{colourLine, colourBoxEdge, colourWarnEdge} {
		if sameColour(c.Style.Fg, colour) {
			return true
		}
	}
	return false
}

// sameColour reports whether a and b are the same colour.
func sameColour(a, b color.Color) bool {
	if a == nil || b == nil {
		return false
	}
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

// openURL opens an address in the browser; tests replace it.
var openURL = desktop.OpenURL

// now is the time, for double-clicks; tests replace it.
var now = time.Now

// mouseDown starts a press: what it does is decided on release.
func (m *Model) mouseDown(mouse tea.Mouse) {
	if mouse.Button != tea.MouseLeft {
		return
	}
	p := point{mouse.X, mouse.Y}
	ms := &m.mouse
	ms.down, ms.from, ms.to, ms.dragged, ms.shown = true, p, p, false, false
	ms.lo, ms.hi = m.spanAt(m.screenLines(), p)
}

// mouseMove extends the selection while the button is down.
func (m *Model) mouseMove(mouse tea.Mouse) {
	ms := &m.mouse
	if !ms.down {
		return
	}
	ms.to = point{mouse.X, mouse.Y}
	if ms.to != ms.from {
		ms.dragged, ms.shown = true, true
	}
}

// mouseUp ends a press: a drag copies what it selected, a click on a link
// the interface drew opens it, and one on an address in log text opens it
// on a second click. A click on a row or a key does what clicking there
// does, and a second click on the same cell of anything else selects and
// copies the word there.
func (m *Model) mouseUp(mouse tea.Mouse) tea.Cmd {
	ms := &m.mouse
	if !ms.down || mouse.Button != tea.MouseLeft {
		return nil
	}
	ms.down = false
	ms.to = point{mouse.X, mouse.Y}
	if ms.to != ms.from {
		ms.dragged, ms.shown = true, true
	}
	if ms.dragged {
		ms.lastAt = time.Time{}
		return m.copySelection()
	}
	lines := m.screenLines()
	if m.quitBox != nil && m.password == nil && !m.quitting {
		// The quit box takes clicks on its buttons and check box, and
		// nothing else: it waits for an answer.
		ms.lastAt = time.Time{}
		return m.quitAnswered(m.quitBox.click(lines, ms.to))
	}
	prev := *ms
	ms.pending, ms.pendingAt = "", time.Time{}
	if url, own, lo, hi := linkAt(lines, ms.to); url != "" {
		ms.lastAt = time.Time{}
		if own {
			if url == m.pageURL() {
				// Nearhost's page opens with its key, so the browser can
				// see the documents it lists. Only the link the interface
				// drew does: an address in log text never gets the key.
				return m.openWith(url, func(string) error { return m.b.OpenPage() })
			}
			return m.open(url)
		}
		// An address in log text came from the network or a dev server, so
		// one click only says where it leads and a second opens it.
		row := rowText(lines[ms.to.y])
		at := m.now()
		if url == prev.pending && ms.to.y == prev.pendingY && lo == prev.pendingLo && hi == prev.pendingHi &&
			row == prev.pendingRow && at.Sub(prev.pendingAt) <= doubleClick {
			return m.open(url)
		}
		toast, ok := m.addressToast(url)
		if !ok {
			return m.showToast(toast, true)
		}
		ms.pending, ms.pendingRow, ms.pendingY, ms.pendingLo, ms.pendingHi, ms.pendingAt = url, row, ms.to.y, lo, hi, at
		return m.showToast(toast, false)
	}
	if m.clickable(mouse) {
		ms.lastAt = time.Time{}
		return m.click(mouse)
	}
	at := now()
	double := ms.to == ms.lastClick && at.Sub(ms.lastAt) <= doubleClick
	ms.lastClick, ms.lastAt = ms.to, at
	if !double {
		return nil
	}
	ms.lastAt = time.Time{}
	if lo, hi, ok := wordAt(lines, ms.to); ok {
		ms.from, ms.to, ms.shown = point{lo, ms.to.y}, point{hi - 1, ms.to.y}, true
		return m.copySelection()
	}
	return nil
}

// addressToast is what the first click on an address in log text says: the
// address with its scheme and host whole, and its path cut to fit the toast.
// With no room for the host it says the address will not open, and ok is
// false.
func (m *Model) addressToast(raw string) (text string, ok bool) {
	const lead = "Click again to open "
	room := m.toastRoom() - ansi.StringWidth(lead)
	u, err := url.Parse(raw)
	if err != nil {
		return "Address too long to show; not opened", false
	}
	if ansi.StringWidth(raw) <= room {
		return lead + raw, true
	}
	origin := u.Scheme + "://" + u.Host
	if ansi.StringWidth(origin)+len("/…") > room {
		return "Address too long to show; not opened", false
	}
	return lead + origin + "/…", true
}

// rowText is the text of a row of cells.
func rowText(row uv.Line) string {
	var sb strings.Builder
	for _, c := range row {
		sb.WriteString(c.Content)
	}
	return sb.String()
}

// open opens a web address in the browser, and says if it could not.
func (m *Model) open(url string) tea.Cmd {
	return m.openWith(url, openURL)
}

// openWith opens url with open, and says so on the row if it fails.
func (m *Model) openWith(url string, open func(string) error) tea.Cmd {
	key := m.sel
	return func() tea.Msg {
		if err := open(url); err != nil {
			return openFailedMsg{key, url, err}
		}
		return nil
	}
}

// openFailedMsg says an address could not be opened, from the row key.
type openFailedMsg struct {
	key, url string
	err      error
}

// clearSelection drops the highlight, as a key, the wheel or a resize
// does.
func (m *Model) clearSelection() {
	m.mouse.shown = false
	m.mouse.pending = ""
}

// copySelection puts the selected text on the clipboard and says so.
func (m *Model) copySelection() tea.Cmd {
	text := m.selectedText(m.screenLines())
	if strings.TrimSpace(text) == "" {
		m.mouse.shown = false
		return nil
	}
	m.mouse.shown = false
	n := strings.Count(text, "\n") + 1
	what := "Copied " + plural(n, "line") + "."
	if n == 1 {
		what = "Copied " + quoteShort(text) + "."
	}
	return tea.Batch(copyText(m.sel, text), m.alert(m.sel, what))
}

// plural is n and noun, with an s when n is not one.
func plural(n int, noun string) string {
	s := strconv.Itoa(n) + " " + noun
	if n != 1 {
		s += "s"
	}
	return s
}

// quoteShort is text in quotes, cut short enough for a toast to show it
// whole.
func quoteShort(text string) string {
	return "“" + ansi.Truncate(text, 40, "…") + "”"
}

// screenLines are the cells of the screen as drawn, without the highlight.
func (m *Model) screenLines() []uv.Line {
	return uv.NewStyledString(m.content()).Lines(ansi.WcWidth)
}

// spanAt is the columns a selection starting at p keeps to, [lo, hi): those
// between the edges nearest p on its row, or the whole row with none.
func (m *Model) spanAt(lines []uv.Line, p point) (int, int) {
	lo, hi := 0, m.width
	if p.y < 0 || p.y >= len(lines) {
		return lo, hi
	}
	row := lines[p.y]
	for x := min(p.x, len(row)) - 1; x >= 0; x-- {
		if isEdge(&row[x]) {
			lo = x + 1
			break
		}
	}
	for x := p.x + 1; x < len(row); x++ {
		if isEdge(&row[x]) {
			hi = x
			break
		}
	}
	return lo, hi
}

// selected reports whether cell x of row y is in the selection.
func (ms *mouseState) selected(x, y int) bool {
	if !ms.shown || x < ms.lo || x >= ms.hi {
		return false
	}
	a, b := ms.from, ms.to
	if b.before(a) {
		a, b = b, a
	}
	if y < a.y || y > b.y {
		return false
	}
	if y == a.y && x < a.x || y == b.y && x > b.x {
		return false
	}
	return true
}

// selectedText is the text of the selection, a line per row with the
// spaces at its end dropped, and without the blank rows at either end. The
// rows after the first lose the indent they share, which is the panel's or
// the box's padding.
func (m *Model) selectedText(lines []uv.Line) string {
	ms := &m.mouse
	a, b := ms.from, ms.to
	if b.before(a) {
		a, b = b, a
	}
	var out []string
	for y := max(a.y, 0); y <= b.y && y < len(lines); y++ {
		var sb strings.Builder
		for x, c := range lines[y] {
			if ms.selected(x, y) {
				sb.WriteString(c.Content)
			}
		}
		out = append(out, strings.TrimRight(sb.String(), " "))
	}
	indent := -1
	for _, l := range out[min(1, len(out)):] {
		if l != "" && (indent < 0 || len(l)-len(strings.TrimLeft(l, " ")) < indent) {
			indent = len(l) - len(strings.TrimLeft(l, " "))
		}
	}
	for i := 1; i < len(out) && indent > 0; i++ {
		if out[i] != "" {
			out[i] = out[i][indent:]
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}

// highlight draws the selection over content, in reverse video.
func (m *Model) highlight(content string) string {
	ms := &m.mouse
	if !ms.shown {
		return content
	}
	rows := strings.Split(content, "\n")
	a, b := ms.from, ms.to
	if b.before(a) {
		a, b = b, a
	}
	for y := max(a.y, 0); y <= b.y && y < len(rows); y++ {
		cells := uv.NewStyledString(rows[y]).Lines(ansi.WcWidth)
		if len(cells) == 0 {
			continue
		}
		line := cells[0]
		for x := range line {
			if ms.selected(x, y) {
				line[x].Style.Attrs |= uv.AttrReverse
			}
		}
		rows[y] = line.Render()
	}
	return strings.Join(rows, "\n")
}

// webAddress is an address in plain text, for a log line that names one
// without a link.
var webAddress = regexp.MustCompile(`https?://[^\s"'<>]+`)

// linkAt is the http or https address at p, and whether it is a link the
// interface drew (own) or an address found in text. Own links are the
// cells drawn with a terminal hyperlink, which carry the address the
// interface holds, not the text shown. Text from logs has its escape
// sequences stripped, so it cannot carry one, and an address in it is only
// found by its look and has to pass safeAddress. Anything else is not
// opened, as a log line from a project could name any scheme.
func linkAt(lines []uv.Line, p point) (url string, own bool, lo, hi int) {
	if p.y < 0 || p.y >= len(lines) || p.x < 0 || p.x >= len(lines[p.y]) {
		return "", false, 0, 0
	}
	row := lines[p.y]
	if url := row[p.x].Link.URL; url != "" {
		if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
			return url, true, 0, 0
		}
		return "", false, 0, 0
	}
	var sb strings.Builder
	col := make([]int, 0, len(row)) // the cell of each byte
	for x, c := range row {
		sb.WriteString(c.Content)
		for range len(c.Content) {
			col = append(col, x)
		}
	}
	text := sb.String()
	for _, loc := range webAddress.FindAllStringIndex(text, -1) {
		end := loc[0] + len(strings.TrimRight(text[loc[0]:loc[1]], ".,;:!?)]}…"))
		if col[loc[0]] > p.x || p.x > col[end-1] {
			continue
		}
		// A row cut short ends in … and may have lost the end of the
		// address, and one that runs to the last cell or up to an edge may
		// have been cut without a mark: what is shown is not the address.
		if strings.Contains(text[loc[0]:loc[1]], "…") || col[loc[1]-1] >= len(row)-1 ||
			col[loc[1]-1]+1 < len(row) && row[col[loc[1]-1]+1].Content == edge {
			return "", false, 0, 0
		}
		if url := text[loc[0]:end]; safeAddress(url) {
			return url, false, col[loc[0]], col[end-1] + 1
		}
	}
	return "", false, 0, 0
}

// safeAddress reports whether an address found in text is one to open: an
// http or https address with a host, in ASCII so a look-alike host is not
// opened, with no control characters or spaces and no user name or
// password, which is how https://nearhost.test@evil.example is made to look
// like a host it is not.
func safeAddress(s string) bool {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' || r > unicode.MaxASCII {
			return false
		}
	}
	u, err := url.Parse(s)
	return err == nil && u.User == nil && u.Hostname() != "" && !strings.Contains(u.Host, "@")
}

// wordAt is the columns of the word at p, [lo, hi): the run of cells around
// it that are neither spaces nor edges.
func wordAt(lines []uv.Line, p point) (int, int, bool) {
	if p.y < 0 || p.y >= len(lines) || p.x < 0 || p.x >= len(lines[p.y]) {
		return 0, 0, false
	}
	row := lines[p.y]
	blank := func(x int) bool {
		return row[x].Content == " " || isEdge(&row[x])
	}
	if blank(p.x) {
		return 0, 0, false
	}
	lo, hi := p.x, p.x+1
	for lo > 0 && !blank(lo-1) {
		lo--
	}
	for hi < len(row) && !blank(hi) {
		hi++
	}
	return lo, hi, true
}
