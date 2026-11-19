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
	if got, own, _, _ := linkAt(lines, point{2, 0}); got != "https://a.test/full/path?x=1" || !own {
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
	m.logs.noteOn(m.sel, "visit https://nearhost.localhost:6327/x now")
	clickOn(t, m, "https://nearhost.localhost:6327/x", 4)
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

// The toast keeps the scheme and host whole and cuts the path; a host too
// long to show is not opened.
func TestAddressToastShowsTheHost(t *testing.T) {
	m, _, _ := clickModel(t)
	room := m.toastRoom() - len("Click again to open ")
	host := "https://" + strings.Repeat("h", room-len("https://.example")-len("/…")-1) + ".example"
	got, ok := m.addressToast(host + "/" + strings.Repeat("p", 80))
	if !ok || got != "Click again to open "+host+"/…" {
		t.Errorf("a long path gave %q, %v", got, ok)
	}
	if got, ok := m.addressToast(host + "/x"); !ok || got != "Click again to open "+host+"/x" {
		t.Errorf("a short path gave %q, %v", got, ok)
	}
	long := "https://" + strings.Repeat("h", room) + ".example"
	if got, ok := m.addressToast(long + "/x"); ok || got != "Address too long to show; not opened" {
		t.Errorf("a host too long gave %q, %v", got, ok)
	}
}

// The same address on two rows is two places: a click on each opens
// nothing, and a second click after the view scrolls opens nothing.
func TestDoubleClickNeedsTheSamePlace(t *testing.T) {
	m, _, opened := clickModel(t)
	m.logs.noteOn(m.sel, "a https://a.example/x one")
	m.logs.noteOn(m.sel, "b https://a.example/x two")
	x, y := find(t, m, "a https://a.example/x one")
	run(m, click(m, x+5, y))
	run(m, click(m, x+5, y+1))
	if len(*opened) != 0 {
		t.Errorf("one click on each of two rows opened %v", *opened)
	}
	run(m, click(m, x+5, y))
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp})
	run(m, click(m, x+5, y))
	if len(*opened) != 0 {
		t.Errorf("a click after the wheel opened %v", *opened)
	}
}

// An address that runs to the last cell or up to an edge may be cut, so it
// is not opened.
func TestAddressAtTheEdgeIsRefused(t *testing.T) {
	for _, row := range []string{"see https://a.test/x", "see https://a.test/x│  "} {
		lines := uv.NewStyledString(row).Lines(ansi.WcWidth)
		if got, _, _, _ := linkAt(lines, point{8, 0}); got != "" {
			t.Errorf("%q gave %q", row, got)
		}
	}
	lines := uv.NewStyledString("see https://a.test/x end").Lines(ansi.WcWidth)
	if got, own, _, _ := linkAt(lines, point{8, 0}); got != "https://a.test/x" || own {
		t.Errorf("an address in the middle of a row gave %q, own %v", got, own)
	}
	if safeAddress("https://nеarhost.test/") {
		t.Error("a host with a Cyrillic letter is safe")
	}
}

// A log line with the page's exact address is text, so it needs two
// clicks.
func TestLogLineWithThePageAddressNeedsTwoClicks(t *testing.T) {
	m, _, opened := clickModel(t)
	m.logs.noteOn(m.sel, "visit https://nearhost.localhost:6327 now")
	clickOn(t, m, "visit https://nearhost.localhost:6327", 8)
	if len(*opened) != 0 {
		t.Errorf("one click opened %v", *opened)
	}
	clickOn(t, m, "visit https://nearhost.localhost:6327", 8)
	if len(*opened) != 1 {
		t.Errorf("two clicks opened %v", *opened)
	}
}
