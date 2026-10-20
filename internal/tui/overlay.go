package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	// boxWidth is the width of a form's box, and boxHeight the least height
	// of a form with steps, in a window that has room for them; a form with
	// more to say grows the box as far as the window allows.
	boxWidth, boxHeight = 76, 22
	// wideBoxWidth is the width of the box of a form with a table to show,
	// in a window that has room for it.
	wideBoxWidth = 96
)

// wideForm is a form drawn in the wide box.
type wideForm interface {
	wide() bool
}

// colourBoxEdge draws the edge of a form's box.
var colourBoxEdge = lipgloss.Color("#4A5449")

// formBox draws the open form in a box over base, the screen drawn faint
// behind it: a terminal has no transparency, so dimming is redrawing.
func (m *Model) formBox(base string, f form) string {
	w := boxWidth
	if wf, ok := f.(wideForm); ok && wf.wide() {
		w = wideBoxWidth
	}
	w = min(w, m.width-4)
	inner := w - 6 // the edges, and two cells of space inside each
	body := f.lines(inner)
	hd := f.heading()
	title := styleTitle.Render(hd.title)
	if hd.step != "" {
		title += styleDim.Render(" · " + hd.step)
	}
	place := ""
	if hd.of > 0 {
		place = styleDim.Render(fmt.Sprintf("%d of %d", hd.at, hd.of))
	}
	top := []string{spread(title, place, inner)}
	if hd.of > 0 {
		top = append(top, progress(hd.at, hd.of, inner))
	}
	top = append(top, "")
	keys := m.help
	keys.SetWidth(inner)
	bottom := []string{"", keys.ShortHelpView(bindings(f.keys()))}
	// A form with steps keeps one height from step to step; a box without,
	// such as the password box or a question, is as tall as what it says.
	h := len(top) + len(body) + len(bottom) + 2
	if hd.of > 0 {
		h = max(boxHeight, h)
	}
	h = min(h, m.height-2)
	room := h - 2 - len(top) - len(bottom)
	if len(body) > room {
		body = body[:room]
	}
	for len(body) < room {
		body = append(body, "")
	}
	lines := append(append(top, body...), bottom...)
	for i, l := range lines {
		lines[i] = fit(l, inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colourBoxEdge).
		Padding(0, 2).Width(w).Render(strings.Join(lines, "\n"))
	x, y := (m.width-w)/2, max((m.height-h)/2, 1)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(faint(base)),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}

// progress is a bar width cells long, filled at of at.
func progress(at, of, width int) string {
	filled := width * at / of
	return styleBeacon.Render(strings.Repeat("━", filled)) + styleFaint.Render(strings.Repeat("━", width-filled))
}

// faint redraws s in the faint colour, its own colours dropped.
func faint(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, l := range lines {
		lines[i] = styleFaint.Render(l)
	}
	return strings.Join(lines, "\n")
}
