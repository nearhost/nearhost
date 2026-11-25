package tui

import (
	"os"
	"strings"

	"github.com/nearhost/nearhost/internal/report"
	rterm "github.com/nearhost/nearhost/internal/report/term"
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
// and the error rendered as the terminal renders a message, wrapped to
// width, when it did not; then that the interface comes back. Colour is
// left out when NO_COLOR is set.
func Told(err error, done string, width int) string {
	if err == nil {
		return "\r\n " + styleBeacon.Render("✓") + " " + done + "\r\n " + styleDim.Render("Back to nearhost…") + "\r\n"
	}
	var b strings.Builder
	rterm.Render(&b, report.From(err), rterm.Options{Width: max(width-1, 20), Color: os.Getenv("NO_COLOR") == ""})
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if l == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, " "+l)
	}
	return "\r\n" + strings.Join(lines, "\r\n") + "\r\n " + styleDim.Render("Back to nearhost…") + "\r\n"
}
