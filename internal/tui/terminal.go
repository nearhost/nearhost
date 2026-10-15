package tui

import (
	"strings"
)

// Asking is what the plain terminal shows when a step that needs root runs
// there instead of in the interface: Nearhost's mark and name, a rule, and
// why, wrapped to width, from the start of a fresh line. sudo's prompt
// follows it. Lines end in \r\n, so they start at the left whatever mode the
// terminal was left in.
func Asking(why string, width int) string {
	width = max(width, 20)
	lines := []string{"", " " + styleMark.Render("∩") + " " + styleTitle.Render("nearhost"), " " + rule(width-2)}
	for _, l := range wrap(clean(why), width-2) {
		lines = append(lines, " "+strings.TrimRight(l, " "))
	}
	return strings.Join(append(lines, "", ""), "\r\n")
}

// Told is what the terminal shows after the step: done when it went well,
// the error in red when it did not, and that the interface comes back.
func Told(err error, done string) string {
	said := styleBeacon.Render("✓") + " " + done
	if err != nil {
		said = styleRed.Render("✗ " + sentence(clean(err.Error())))
	}
	return "\r\n " + said + "\r\n " + styleDim.Render("Back to nearhost…") + "\r\n"
}
