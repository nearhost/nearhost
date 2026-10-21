package tui

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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
}

// doubleClick is the longest time between the clicks of a double-click.
const doubleClick = 400 * time.Millisecond

// edge is the vertical line between panels and on the sides of a box.
const edge = "│"

// openURL opens an address in the browser; tests replace it.
var openURL = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, url).Run()
}

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

// mouseUp ends a press: a drag copies what it selected, a click on an
// address opens it, one on a row or a key does what clicking there does,
// and a second click on the same cell of anything else selects and copies
// the word there.
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
	if url := linkAt(lines, ms.to); url != "" {
		ms.lastAt = time.Time{}
		key := m.sel
		return func() tea.Msg {
			if err := openURL(url); err != nil {
				return openFailedMsg{key, url, err}
			}
			return nil
		}
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

// openFailedMsg says an address could not be opened, from the row key.
type openFailedMsg struct {
	key, url string
	err      error
}

// clearSelection drops the highlight, as a key, the wheel or a resize
// does.
func (m *Model) clearSelection() {
	m.mouse.shown = false
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
		if row[x].Content == edge {
			lo = x + 1
			break
		}
	}
	for x := p.x + 1; x < len(row); x++ {
		if row[x].Content == edge {
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

// linkAt is the http or https address at p: the link the cell is drawn
// with, or an address written there. Anything else is not opened, as a log
// line from a project could name any scheme.
func linkAt(lines []uv.Line, p point) string {
	if p.y < 0 || p.y >= len(lines) || p.x < 0 || p.x >= len(lines[p.y]) {
		return ""
	}
	row := lines[p.y]
	url := row[p.x].Link.URL
	if url == "" {
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
			if col[loc[0]] <= p.x && p.x <= col[end-1] {
				url = text[loc[0]:end]
			}
		}
	}
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return url
	}
	return ""
}

// wordAt is the columns of the word at p, [lo, hi): the run of cells around
// it that are neither spaces nor edges.
func wordAt(lines []uv.Line, p point) (int, int, bool) {
	if p.y < 0 || p.y >= len(lines) || p.x < 0 || p.x >= len(lines[p.y]) {
		return 0, 0, false
	}
	row := lines[p.y]
	blank := func(x int) bool {
		c := row[x].Content
		return c == " " || c == edge
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
