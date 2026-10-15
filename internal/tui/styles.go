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
	// colourLine draws rules and borders; colourSelected fills the selected
	// row; colourFaint is the screen behind an open form.
	colourLine     = lipgloss.Color("#2A312B")
	colourSelected = lipgloss.Color("#1F261F")
	colourFaint    = lipgloss.Color("#3A413B")
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colourText)
	styleText     = lipgloss.NewStyle().Foreground(colourText)
	styleDim      = lipgloss.NewStyle().Foreground(colourDim)
	styleBeacon   = lipgloss.NewStyle().Foreground(colourBeacon)
	styleAmber    = lipgloss.NewStyle().Foreground(colourAmber)
	styleRed      = lipgloss.NewStyle().Foreground(colourRed)
	styleSelected = lipgloss.NewStyle().Foreground(colourGround).Background(colourBeacon)
	styleLabel    = lipgloss.NewStyle().Foreground(colourDim).Width(labelWidth)
	styleMark     = lipgloss.NewStyle().Bold(true).Foreground(colourBeacon)
	styleRule     = lipgloss.NewStyle().Foreground(colourLine)
	styleFaint    = lipgloss.NewStyle().Foreground(colourFaint)
)

// labelWidth is the width of the labels of a panel's fields.
const labelWidth = 10

// stateMark is the dot and colour for a project state.
func stateMark(st engine.State) seg {
	switch st {
	case engine.StateRunning:
		return seg{styleBeacon, "●"}
	case engine.StateStarting, engine.StateDegraded:
		return seg{styleAmber, "◐"}
	case engine.StateError:
		return seg{styleRed, "●"}
	}
	return seg{styleDim, "○"}
}

// serverMark is the dot and colour for a server's connection.
func serverMark(st transport.Status) seg {
	switch st {
	case transport.StatusConnected:
		return seg{styleBeacon, "●"}
	case transport.StatusConnecting, transport.StatusReconnecting:
		return seg{styleAmber, "◐"}
	case transport.StatusAuthFailed:
		return seg{styleRed, "○"}
	}
	return seg{styleDim, "○"}
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
