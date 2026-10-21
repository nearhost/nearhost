package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
)

// reversed matches an SGR sequence that turns reverse video on.
var reversed = regexp.MustCompile(`\x1b\[(?:[0-9:]*;)*7(?:;[0-9:;]*)?m`)

// find is the cell where text first starts on the screen.
func find(t *testing.T, m *Model, text string) (int, int) {
	t.Helper()
	for y, l := range strings.Split(screen(m), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q is not on the screen:\n%s", text, screen(m))
	return 0, 0
}

// drag presses at one cell, moves to another and releases there, and
// returns what the release does.
func drag(m *Model, x0, y0, x1, y1 int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	return cmd
}

// clipboard is what cmd puts on the clipboard, or "" when it puts nothing.
func clipboard(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	return clipboardOf(cmd)
}

// opens replaces openURL for the test and returns what it was asked to open.
func opens(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	old := openURL
	openURL = func(url string) error { opened = append(opened, url); return nil }
	t.Cleanup(func() { openURL = old })
	return &opened
}

// A drag across rows of the Home panel copies the panel's text alone, not
// the sidebar beside it, highlights it, and says so in the log. A key
// drops the highlight.
func TestDragCopiesTheTextOfOnePanel(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	run(m, func() tea.Msg { return startedMsg{} })
	x0, y0 := find(t, m, "proxy     ●")
	x1, y1 := find(t, m, "web UI    https://localhost:6327")
	got := clipboard(drag(m, x0, y0, x1+len("web UI    https://localhost:6327")-1, y1))
	if got != "proxy     ● https :443 · http :80\nweb UI    https://localhost:6327" {
		t.Errorf("copied %q", got)
	}
	if !reversed.MatchString(m.View().Content) {
		t.Error("the selection is not highlighted")
	}
	wantScreen(t, m, "Copied 2 lines.")
	press(m, "j")
	if reversed.MatchString(m.View().Content) {
		t.Error("a key left the selection highlighted")
	}
}

// A drag within one row copies the cells it covers, either way round.
func TestDragWithinARow(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	run(m, func() tea.Msg { return startedMsg{} })
	x, y := find(t, m, "https://localhost:6327")
	end := x + len("https://localhost:6327") - 1
	if got := clipboard(drag(m, end, y, x, y)); got != "https://localhost:6327" {
		t.Errorf("copied %q", got)
	}
	wantScreen(t, m, `Copied “https://localhost:6327”.`)
}

// A click on a link opens it, and so does one on an address a log line
// names in plain text, without what ends the sentence. A drag that starts
// on a link selects instead.
func TestClickOpensAddresses(t *testing.T) {
	opened := opens(t)
	m := sized(newFake(t, sampleConfig()))
	run(m, func() tea.Msg { return startedMsg{} })
	x, y := find(t, m, "https://localhost:6327")
	run(m, click(m, x+3, y))
	m.logs.noteOn(m.sel, "Docs at https://nearhost.dev/docs/.")
	m.logs.noteOn(m.sel, "Or file:///etc/passwd.")
	x, y = find(t, m, "https://nearhost.dev/docs/")
	run(m, click(m, x+10, y))
	fx, fy := find(t, m, "file:///etc/passwd")
	run(m, click(m, fx+2, fy))
	if strings.Join(*opened, " ") != "https://localhost:6327 https://nearhost.dev/docs/" {
		t.Errorf("opened %v", *opened)
	}
	if got := clipboard(drag(m, x, y, x+7, y)); got != "https://" || len(*opened) != 2 {
		t.Errorf("drag from a link copied %q and opened %v", got, *opened)
	}
}

// A double-click copies the word under it, but not on a sidebar row or a
// footer key, where each click does what clicking there does.
func TestDoubleClickCopiesAWord(t *testing.T) {
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
	f := newFake(t, sampleConfig())
	f.path = "/etc/nearhost/config.yaml"
	m := sized(f)
	x, y := find(t, m, "/etc/nearhost/config.yaml")
	if got := clipboard(click(m, x+2, y)); got != "" {
		t.Errorf("one click copied %q", got)
	}
	if got := clipboard(click(m, x+2, y)); got != "/etc/nearhost/config.yaml" {
		t.Errorf("double-click copied %q", got)
	}
	click(m, x+2, y)
	at = at.Add(time.Second)
	if got := clipboard(click(m, x+2, y)); got != "" {
		t.Errorf("clicks a second apart copied %q", got)
	}
	sx, sy := find(t, m, "website")
	click(m, sx, sy)
	if got := clipboard(click(m, sx, sy)); got != "" || m.sel != rowKey(rowProject, "website") {
		t.Errorf("double-click on a row copied %q, selected %q", got, m.sel)
	}
}

// A drag that starts on a footer key copies, and does not run the key.
func TestDragFromTheFooterDoesNotRunItsKey(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	click(m, 3, bodyTop+3) // website
	x, y := find(t, m, "o open")
	run(m, drag(m, x, y, x+5, y))
	if len(f.opened) != 0 {
		t.Errorf("the drag opened %v", f.opened)
	}
}

// In a form, a drag across rows keeps to the box, leaving out what is
// drawn beside it. The blank row it ends on is left out.
func TestDragInAFormKeepsToTheBox(t *testing.T) {
	m := sized(newFake(t, &config.Config{}))
	press(m, "s")
	x0, y0 := find(t, m, "Add server")
	lines := strings.Split(screen(m), "\n")
	y1 := y0 + 2
	got := clipboard(drag(m, x0, y0, ansi.StringWidth(lines[y1])-1, y1))
	if strings.Contains(got, "│") || !strings.HasPrefix(got, "Add server") || strings.Count(got, "\n") != 1 {
		t.Errorf("copied %q", got)
	}
}

// Only web addresses open: a link drawn to another scheme is left alone.
func TestLinkAtOpensOnlyWebAddresses(t *testing.T) {
	lines := uv.NewStyledString(ansi.SetHyperlink("file:///etc/passwd") + "passwd" + ansi.ResetHyperlink() + " " +
		ansi.SetHyperlink("https://a.test") + "a.test" + ansi.ResetHyperlink()).Lines(ansi.WcWidth)
	if got := linkAt(lines, point{2, 0}); got != "" {
		t.Errorf("a file: link opens %q", got)
	}
	if got := linkAt(lines, point{9, 0}); got != "https://a.test" {
		t.Errorf("the https link opens %q", got)
	}
}

// Blank rows at either end of a drag are left out.
func TestDragLeavesOutBlankRows(t *testing.T) {
	m := sized(newFake(t, &config.Config{}))
	x, y := find(t, m, "Nothing to serve yet.")
	if got := clipboard(drag(m, 0, y-1, x+len("Nothing to serve yet.")-1, y)); got != "Nothing to serve yet." {
		t.Errorf("copied %q", got)
	}
}

// A copy goes to the terminal over OSC 52 and to the system's own tool, as
// some terminals have no OSC 52.
func TestCopyTextUsesTheSystemClipboardToo(t *testing.T) {
	var got []string
	old := systemClipboard
	systemClipboard = func(text string) error { got = append(got, text); return nil }
	t.Cleanup(func() { systemClipboard = old })
	cmd := copyText("ssh mini")
	if s := clipboard(cmd); s != "ssh mini" {
		t.Errorf("OSC 52 got %q", s)
	}
	for _, c := range cmd().(tea.BatchMsg) {
		if c != nil {
			c()
		}
	}
	if strings.Join(got, ",") != "ssh mini" {
		t.Errorf("the system clipboard got %v", got)
	}
}
