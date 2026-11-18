package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The log view's footer at 80 columns starts with its first keys, and ?
// lists every key, then closes with the log's place and search kept.
func TestLogViewListsItsKeys(t *testing.T) {
	m, _ := logModel(t, 5)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	lines := strings.Split(screen(m), "\n")
	foot := lines[len(lines)-1]
	if !strings.Contains(foot, "j/k scroll · enter expand · tab kind · / search · ? keys") {
		t.Errorf("footer %q", foot)
	}
	press(m, "/")
	typeText(m, "page=3")
	press(m, "enter")
	press(m, "?")
	if !m.showHelp {
		t.Fatal("? did not open the help in the log view")
	}
	wantScreen(t, m, "j/k", "pgup/pgdn", "shift+tab", "reveal cookies", "copy curl", "replay", "follow", "top", "wrap", "back", "close this list")
	press(m, "?")
	if m.showHelp || m.logView == nil || m.logView.search.Value() != "page=3" {
		t.Fatalf("help %v, log %v after closing it", m.showHelp, m.logView)
	}
	press(m, "?")
	press(m, "esc")
	if m.showHelp || m.logView == nil {
		t.Error("esc over the help left the log, or left the help open")
	}
}

// ? typed in the log search is text.
func TestLogSearchTakesQuestionMark(t *testing.T) {
	m, _ := logModel(t, 3)
	press(m, "/")
	typeText(m, "a?")
	if m.showHelp || m.logView.search.Value() != "a?" {
		t.Errorf("help %v, search %q", m.showHelp, m.logView.search.Value())
	}
}

// A paste reaches the log search while it is typed in, as one line, and the
// filter's limit holds.
func TestPasteIntoLogSearch(t *testing.T) {
	m, _ := logModel(t, 3)
	m.Update(tea.PasteMsg{Content: "ignored"})
	if m.logView.search.Value() != "" {
		t.Fatal("a paste reached a search that was not focused")
	}
	press(m, "/")
	m.Update(tea.PasteMsg{Content: "\r\npage=2\x1b[2J\nsecond"})
	if got := m.logView.search.Value(); got != "page=2" {
		t.Errorf("search %q", got)
	}
	if m.logView.searching != true {
		t.Error("the paste ended the search")
	}
}

// A paste reaches the sidebar filter while it has the focus, as the first
// line, and not otherwise.
func TestPasteIntoFilter(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	m.Update(tea.PasteMsg{Content: "myapp"})
	if m.filter.Value() != "" {
		t.Fatal("a paste reached a filter that was not focused")
	}
	press(m, "/")
	m.Update(tea.PasteMsg{Content: "my\x07app\nsecond line\n"})
	if got := m.filter.Value(); got != "my app" || !m.filtering {
		t.Errorf("filter %q, filtering %v", got, m.filtering)
	}
	m.Update(tea.PasteMsg{Content: strings.Repeat("x", 200)})
	if n := len([]rune(m.filter.Value())); n != 64 {
		t.Errorf("filter holds %d runes, want its limit of 64", n)
	}
}

// A paste into a window too small stays out of the filter.
func TestPasteIntoFilterWhileSmall(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	press(m, "/")
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	m.Update(tea.PasteMsg{Content: "abc"})
	if m.filter.Value() != "" {
		t.Errorf("filter %q", m.filter.Value())
	}
}

// A paste never acts on a request box, in or after its guard.
func TestPasteDoesNothingToARequestBox(t *testing.T) {
	m, f, _ := projectFormModel(t)
	stopClock(m)
	settle(m, func() tea.Msg { return passMsg{passReq(m)} })
	for _, d := range []time.Duration{100 * time.Millisecond, 700 * time.Millisecond} {
		wait(m, d)
		settle(m, func() tea.Msg { return tea.PasteMsg{Content: "y"} })
		settle(m, func() tea.Msg { return tea.PasteMsg{Content: "A\n"} })
	}
	if got := f.passthroughCalls(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	if m.filter.Value() != "" {
		t.Errorf("filter %q", m.filter.Value())
	}
}
