package tui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// The brand palette, and the colours of states.
var (
	colourGround = lipgloss.Color("#0F1210")
	colourBeacon = lipgloss.Color("#D6F26B")
	colourText   = lipgloss.Color("#ECEFE8")
	colourDim    = lipgloss.Color("#7C8479")
	colourAmber  = lipgloss.Color("#F2B84B")
	colourRed    = lipgloss.Color("#F26B6B")
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colourText)
	styleDim      = lipgloss.NewStyle().Foreground(colourDim)
	styleBeacon   = lipgloss.NewStyle().Foreground(colourBeacon)
	styleAmber    = lipgloss.NewStyle().Foreground(colourAmber)
	styleRed      = lipgloss.NewStyle().Foreground(colourRed)
	styleSelected = lipgloss.NewStyle().Foreground(colourGround).Background(colourBeacon)
	styleLabel    = lipgloss.NewStyle().Foreground(colourDim).Width(labelWidth)
)

// labelWidth is the width of the labels of a panel's fields.
const labelWidth = 10

// stateDot is the dot and colour for a project state.
func stateDot(st engine.State) string {
	switch st {
	case engine.StateRunning:
		return styleBeacon.Render("●")
	case engine.StateStarting, engine.StateDegraded:
		return styleAmber.Render("◐")
	case engine.StateError:
		return styleRed.Render("●")
	default:
		return styleDim.Render("○")
	}
}

// serverDot is the dot and colour for a server's connection.
func serverDot(st transport.Status) string {
	switch st {
	case transport.StatusConnected:
		return styleBeacon.Render("●")
	case transport.StatusConnecting, transport.StatusReconnecting:
		return styleAmber.Render("◐")
	case transport.StatusAuthFailed:
		return styleRed.Render("○")
	default:
		return styleDim.Render("○")
	}
}

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
