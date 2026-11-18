package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/nearhost/nearhost/internal/proxy"
)

// clickModel is a sized model with a clock the test moves, and the opener
// replaced.
func clickModel(t *testing.T) (*Model, *time.Time, *[]string) {
	t.Helper()
	opened := opens(t)
	m := sized(newFake(t, sampleConfig()))
	run(m, func() tea.Msg { return startedMsg{} })
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	return m, &at, opened
}

// clickOn clicks the cell dx right of where text starts on the screen and
// runs what the click does.
func clickOn(t *testing.T, m *Model, text string, dx int) {
	t.Helper()
	x, y := find(t, m, text)
	run(m, click(m, x+dx, y))
}

// A single click on an address in log text opens nothing and says what a
// second click opens; the second click opens it.
func TestLogAddressNeedsADoubleClick(t *testing.T) {
	m, _, opened := clickModel(t)
	m.logs.noteOn(m.sel, "GET https://evil.example/a?b=1 done")
	clickOn(t, m, "https://evil.example/a?b=1", 5)
	if len(*opened) != 0 {
		t.Fatalf("one click opened %v", *opened)
	}
	wantScreen(t, m, "Click again to open https://evil.example/a?b=1")
	clickOn(t, m, "https://evil.example/a?b=1", 9)
	if strings.Join(*opened, " ") != "https://evil.example/a?b=1" {
		t.Errorf("a double click opened %v", *opened)
	}
	clickOn(t, m, "https://evil.example/a?b=1", 5)
	if len(*opened) != 1 {
		t.Errorf("a third click opened again: %v", *opened)
	}
}

// A link the interface drew opens on one click, with the address it holds
// and not the text shown.
func TestOwnLinkOpensOnOneClickWithItsFullTarget(t *testing.T) {
	lines := uv.NewStyledString(ansi.SetHyperlink("https://a.test/full/path?x=1") + "a.test…" + ansi.ResetHyperlink()).Lines(ansi.WcWidth)
	if got, own := linkAt(lines, point{2, 0}); got != "https://a.test/full/path?x=1" || !own {
		t.Errorf("the link opens %q, own %v", got, own)
	}
	m, _, opened := clickModel(t)
	clickOn(t, m, "https://nearhost.localhost:6327", 3)
	// Nearhost's own page opens through the backend, with its key.
	if f := m.b.(*fakeBackend); len(*opened) != 0 || strings.Join(f.opened, " ") != "page" {
		t.Errorf("one click on the page address opened %v and the backend %v", *opened, f.opened)
	}
}

// A log line that holds the same address as an interface link is still
// text: it needs a double click.
func TestLogLineCannotImitateAnInterfaceLink(t *testing.T) {
	m, _, opened := clickModel(t)
	m.logs.noteOn(m.sel, "visit https://localhost:6327/x now")
	clickOn(t, m, "https://localhost:6327/x", 4)
	if len(*opened) != 0 {
		t.Errorf("one click opened %v", *opened)
	}
	// A hyperlink sequence in log text is stripped, not drawn as a link.
	m.logs.noteOn(m.sel, ansi.SetHyperlink("https://evil.example")+"harmless"+ansi.ResetHyperlink()+" words")
	clickOn(t, m, "harmless", 2)
	if len(*opened) != 0 {
		t.Errorf("a hyperlink in log text opened %v", *opened)
	}
}

// Two clicks that are not on the same address, or are a second apart, open
// nothing.
func TestDoubleClickNeedsTheSameAddressAtOnce(t *testing.T) {
	m, at, opened := clickModel(t)
	m.logs.noteOn(m.sel, "one https://a.example/x two https://b.example/y end")
	clickOn(t, m, "https://a.example/x", 3)
	clickOn(t, m, "https://b.example/y", 3)
	if len(*opened) != 0 {
		t.Errorf("clicks on different addresses opened %v", *opened)
	}
	*at = at.Add(time.Second)
	clickOn(t, m, "https://b.example/y", 3)
	if len(*opened) != 0 {
		t.Errorf("clicks a second apart opened %v", *opened)
	}
	// A click elsewhere between the two drops the first.
	*at = at.Add(50 * time.Millisecond)
	clickOn(t, m, "one", 1)
	clickOn(t, m, "https://b.example/y", 3)
	if len(*opened) != 0 {
		t.Errorf("a click elsewhere between two opened %v", *opened)
	}
}

// An address cut off by the row never opens, on any number of clicks.
func TestCutOffAddressNeverOpens(t *testing.T) {
	m, _, opened := clickModel(t)
	m.logs.add(logEntry{at: m.now(), source: "myapp", req: &proxy.Access{Method: "GET", Status: 200,
		URI: "/go?u=https://evil.example/" + strings.Repeat("a", 300)}}, m.sel)
	x, y := find(t, m, "https://evil.example/aaa")
	if !strings.Contains(strings.Split(screen(m), "\n")[y], "…") {
		t.Fatalf("the row is not cut:\n%s", screen(m))
	}
	for range 3 {
		run(m, click(m, x+4, y))
	}
	if len(*opened) != 0 {
		t.Errorf("a cut address opened %v", *opened)
	}
}

// An address with a user name, a control character or a space in it never
// opens, as https://nearhost.test@evil.example is a disguise.
func TestUnsafeAddressNeverOpens(t *testing.T) {
	for _, bad := range []string{
		"https://nearhost.test@evil.example/",
		"https://user:pw@evil.example/",
		"https://evil.example\\@nearhost.test/",
		"https:///nohost",
	} {
		m, _, opened := clickModel(t)
		m.logs.noteOn(m.sel, "see "+bad+" end")
		x, y := find(t, m, bad)
		for range 3 {
			run(m, click(m, x+10, y))
		}
		if len(*opened) != 0 {
			t.Errorf("%q opened %v", bad, *opened)
		}
	}
	if safeAddress("https://a.test/x\x00y") || safeAddress("https://a.test/x y") || !safeAddress("https://a.test/x?y=1@z") {
		t.Error("safeAddress is wrong about a control character, a space or an @ in a query")
	}
}
