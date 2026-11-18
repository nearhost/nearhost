package tui

import (
	"image/color"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// palette is a set of the interface's colours, for one kind of terminal
// background.
type palette struct {
	ground  color.Color // text on a fill of the beacon colour
	beacon  color.Color // the accent: keys, links, the mark
	text    color.Color // body text
	dim     color.Color // muted text and labels
	amber   color.Color // warnings and states on their way
	red     color.Color // errors
	running color.Color // a running project, a connected server
	// line draws rules; selected fills the selected row; faint is the
	// screen behind an open form; boxEdge and warnEdge draw the edges of
	// boxes and of advice.
	line     color.Color
	selected color.Color
	faint    color.Color
	boxEdge  color.Color
	warnEdge color.Color
}

// darkPalette is the brand palette, for a dark terminal. It is what the
// interface starts with.
var darkPalette = palette{
	ground:   lipgloss.Color("#0F1210"),
	beacon:   lipgloss.Color("#D6F26B"),
	text:     lipgloss.Color("#ECEFE8"),
	dim:      lipgloss.Color("#7C8479"),
	amber:    lipgloss.Color("#F2B84B"),
	red:      lipgloss.Color("#F26B6B"),
	running:  lipgloss.Color("#D6F26B"),
	line:     lipgloss.Color("#2A312B"),
	selected: lipgloss.Color("#1F261F"),
	faint:    lipgloss.Color("#3A413B"),
	boxEdge:  lipgloss.Color("#4A5449"),
	warnEdge: lipgloss.Color("#6B5524"),
}

// lightPalette is the palette for a terminal with a light background. Its
// text reads at 4.5:1 or better on white and on the selected row. The edges
// differ from every text colour, as isEdge tells them apart by colour.
var lightPalette = palette{
	ground:   lipgloss.Color("#FFFFFF"),
	beacon:   lipgloss.Color("#4D6B00"),
	text:     lipgloss.Color("#1F2420"),
	dim:      lipgloss.Color("#5B635A"),
	amber:    lipgloss.Color("#8A5A00"),
	red:      lipgloss.Color("#B3261E"),
	running:  lipgloss.Color("#1B7F3B"),
	line:     lipgloss.Color("#C5CBC2"),
	selected: lipgloss.Color("#F0F4E6"),
	faint:    lipgloss.Color("#B5BBB2"),
	boxEdge:  lipgloss.Color("#7A8277"),
	warnEdge: lipgloss.Color("#A47B2E"),
}

// The colours in use, set by usePalette.
var (
	colourGround, colourBeacon, colourText, colourDim color.Color
	colourAmber, colourRed, colourRunning, colourLine color.Color
	colourSelected, colourFaint, colourBoxEdge        color.Color
	colourWarnEdge                                    color.Color
)

// The styles in use, set by usePalette.
var (
	styleTitle, styleText, styleDim, styleBeacon, styleAmber lipgloss.Style
	styleRed, styleRunning, styleSelected, styleLabel        lipgloss.Style
	styleMark, styleRule, styleFaint                         lipgloss.Style
	// styleBox draws a scrollbar on the edge of a box.
	styleBox lipgloss.Style
)

func init() { usePalette(darkPalette) }

// usePalette sets every colour and style of the interface from p, and
// draws again what is drawn once and kept, such as the mark. It changes
// package state: call it only from the model's Update, or from a test that
// does not run in parallel.
func usePalette(p palette) {
	colourGround, colourBeacon, colourText, colourDim = p.ground, p.beacon, p.text, p.dim
	colourAmber, colourRed, colourRunning, colourLine = p.amber, p.red, p.running, p.line
	colourSelected, colourFaint, colourBoxEdge, colourWarnEdge = p.selected, p.faint, p.boxEdge, p.warnEdge

	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(colourText)
	styleText = lipgloss.NewStyle().Foreground(colourText)
	styleDim = lipgloss.NewStyle().Foreground(colourDim)
	styleBeacon = lipgloss.NewStyle().Foreground(colourBeacon)
	styleAmber = lipgloss.NewStyle().Foreground(colourAmber)
	styleRed = lipgloss.NewStyle().Foreground(colourRed)
	styleRunning = lipgloss.NewStyle().Foreground(colourRunning)
	styleSelected = lipgloss.NewStyle().Foreground(colourGround).Background(colourBeacon)
	styleLabel = lipgloss.NewStyle().Foreground(colourDim).Width(labelWidth)
	styleMark = lipgloss.NewStyle().Bold(true).Foreground(colourBeacon)
	styleRule = lipgloss.NewStyle().Foreground(colourLine)
	styleFaint = lipgloss.NewStyle().Foreground(colourFaint)
	styleBox = styleDim.Foreground(colourBoxEdge)
	drawMark()
}

// labelWidth is the width of the labels of a panel's fields.
const labelWidth = 10

// markKind is a state a mark can show. Each has its own glyph as well as its
// own colour, so the states differ with colour off.
type markKind int

const (
	markStopped   markKind = iota // off, or not connected: ○
	markRunning                   // running or connected: ●
	markError                     // an error or a failed login: ✕
	markAttention                 // waiting for approval, or a warning: !
)

// glyphs are the glyphs of the mark kinds, each one cell wide.
var glyphs = map[markKind]string{
	markStopped:   "○",
	markRunning:   "●",
	markError:     "✕",
	markAttention: "!",
}

// glyph is the mark of a kind: its glyph in its colour. Every state mark of
// the interface comes from here, so the sidebar, the headers and the detail
// panels agree.
func glyph(k markKind) seg {
	style := styleDim
	switch k {
	case markRunning:
		style = styleRunning
	case markError:
		style = styleRed
	case markAttention:
		style = styleAmber
	}
	return seg{style, glyphs[k]}
}

// busyMark is the mark of a project or server that is on its way, where the
// spinner is not turning.
func busyMark() seg { return seg{styleAmber, "◐"} }

// stateMark is the mark and colour for a project state.
func stateMark(st engine.State) seg {
	switch st {
	case engine.StateRunning:
		return glyph(markRunning)
	case engine.StateStarting, engine.StateDegraded:
		return busyMark()
	case engine.StateError:
		return glyph(markError)
	}
	return glyph(markStopped)
}

// serverMark is the mark and colour for a server's connection.
func serverMark(st transport.Status) seg {
	switch st {
	case transport.StatusConnected:
		return glyph(markRunning)
	case transport.StatusConnecting, transport.StatusReconnecting:
		return busyMark()
	case transport.StatusAuthFailed:
		return glyph(markError)
	}
	return glyph(markStopped)
}

// projectDot is the mark of a project: the spinner while it starts, the
// attention mark while its .nearhostrc waits for approval (an error still
// shows its own mark), and its state's mark otherwise.
func (m *Model) projectDot(st engine.ProjectStatus) seg {
	if st.State != engine.StateStarting && st.State != engine.StateError && st.Pending != nil {
		return glyph(markAttention)
	}
	return m.stateDot(st.State)
}

// stateDot is stateMark, with the spinner for a project that is starting.
func (m *Model) stateDot(st engine.State) seg {
	if st == engine.StateStarting {
		return seg{styleAmber, m.spinner.View()}
	}
	return stateMark(st)
}

// serverDot is serverMark, with the spinner for a server that is
// connecting.
func (m *Model) serverDot(st transport.Status) seg {
	if st == transport.StatusConnecting || st == transport.StatusReconnecting {
		return seg{styleAmber, m.spinner.View()}
	}
	return serverMark(st)
}

// dot draws a seg on its own.
func (sg seg) String() string { return sg.style.Render(sg.text) }

// clean makes text from outside, such as a log line or a label, safe to
// draw: escape sequences are removed, control characters become spaces and
// invisible format characters (such as a right-to-left override) are
// dropped, so nothing can move the cursor, break the layout or change how
// the text reads.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// wrap breaks s into lines of at most width cells, at spaces, slashes and
// hyphens where it can.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	return strings.Split(ansi.Wrap(s, width, "/-"), "\n")
}

// field is a labelled value of a panel, wrapped to width with its later
// lines indented under the first.
func field(label, value string, width int) []string {
	lines := wrap(value, width-labelWidth)
	out := make([]string, len(lines))
	for i, l := range lines {
		lead := strings.Repeat(" ", labelWidth)
		if i == 0 {
			lead = styleLabel.Render(label)
		}
		out[i] = lead + l
	}
	return out
}

// fit cuts s to width cells and pads it to exactly that width.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	if pad := width - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// spread draws left at the start and right at the end of exactly width
// cells, cutting left short when both do not fit.
func spread(left, right string, width int) string {
	rw := ansi.StringWidth(right)
	if rw == 0 {
		return fit(left, width)
	}
	if rw >= width {
		return fit(right, width)
	}
	return fit(left, width-rw-1) + " " + right
}

// rule is a horizontal line width cells long.
func rule(width int) string {
	return styleRule.Render(strings.Repeat("─", max(width, 0)))
}

// section heads a part of a panel: its label, a rule, and what is said on
// the right, exactly width cells.
func section(label, right string, width int) string {
	head := styleDim.Render(label + " ")
	if right != "" {
		right = " " + right
	}
	gap := width - ansi.StringWidth(head) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(head+right, width)
	}
	return head + rule(gap) + right
}
