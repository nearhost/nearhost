package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/transport"
)

// passModel has apollo, with two open passthroughs (the second listed
// first) and a grant for Claude Code, and mini; apollo is selected.
func passModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	cfg := sampleConfig()
	cfg.Servers = append(cfg.Servers, config.Server{Name: "apollo", SSHHost: "apollo.lan"})
	f := newFake(t, cfg)
	f.servers["apollo"] = transport.StatusConnected
	m := sized(f)
	f.passes = []engine.Passthrough{
		{PassthroughSpec: engine.PassthroughSpec{ID: "ffff0000aaaa", Server: "apollo", Requester: "codex", Remote: 5173}, Local: 5173,
			Opened: m.now().Add(-2 * time.Hour)},
		{PassthroughSpec: engine.PassthroughSpec{ID: "id-not-shown", Session: "1a2b3c4d5e6f7a8b", Server: "apollo", Requester: "claude-code", Label: "browser preview", Remote: 41873},
			Local: 41873, Opened: m.now().Add(-12 * time.Minute)},
	}
	f.grantList = []grants.Grant{{Server: "apollo", Requester: "claude-code", Kind: grants.KindPassthrough,
		Created: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)}}
	m.selectRow(rowKey(rowServer, "apollo"))
	run(m, m.fetchGrants()) // the grants are read on the worker
	return m, f
}

func footer(m *Model) string {
	var parts []string
	for _, it := range m.footerItems() {
		parts = append(parts, it.key+" "+it.label)
	}
	return strings.Join(parts, " · ")
}

func TestServerPanelListsPassthroughs(t *testing.T) {
	m, _ := passModel(t)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	s := screenText(m)
	for _, want := range []string{
		"passthroughs",
		"5173 → localhost:5173 · Codex · 2h",
		"41873 → localhost:41873 · Claude Code · browser preview · 1a2b3c4d · 12m",
		"always allowed",
		"Claude Code · since 27 Oct",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("panel lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "5173 → localhost:5173") > strings.Index(s, "41873 → localhost:41873") {
		t.Errorf("passthroughs are not sorted by port:\n%s", s)
	}
}

func TestServerPassFocus(t *testing.T) {
	m, f := passModel(t)
	if got := footer(m); !strings.Contains(got, "tab passthroughs") {
		t.Fatalf("footer %q", got)
	}
	press(m, "tab")
	if !m.passFocus {
		t.Fatal("tab did not focus the passthroughs")
	}
	if got := footer(m); got != "↑↓ select · x close · tab done" {
		t.Fatalf("footer %q", got)
	}
	press(m, "down")
	run(m, press(m, "x")) // 41873, the second passthrough
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"close id-not-shown"}) {
		t.Fatalf("calls %v", got)
	}
	press(m, "down") // the grant
	if got := footer(m); got != "↑↓ select · x revoke · tab done" {
		t.Fatalf("footer %q", got)
	}
	press(m, "x")
	wantScreen(t, m, "Stop always allowing Claude Code on apollo?", "Open passthroughs stay open.")
	if got := footer(m); got != "y revoke · n keep" {
		t.Fatalf("footer %q", got)
	}
	settle(m, press(m, "y"))
	want := []string{"close id-not-shown", "revoke apollo claude-code"}
	if got := f.passthroughCalls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
	if m.form != nil {
		t.Fatalf("the question stays open: %T", m.form)
	}
	press(m, "tab")
	if m.passFocus {
		t.Error("tab did not give the focus back")
	}
}

func TestServerPassKeepGrant(t *testing.T) {
	m, f := passModel(t)
	press(m, "tab")
	press(m, "down")
	press(m, "down")
	press(m, "x")
	settle(m, press(m, "n"))
	if m.form != nil || len(f.passthroughCalls()) != 0 {
		t.Fatalf("form %v, calls %v", m.form, f.passthroughCalls())
	}
}

func TestServerPassFailureShown(t *testing.T) {
	m, f := passModel(t)
	f.passErr = errBoom
	press(m, "tab")
	settle(m, press(m, "x"))
	wantScreen(t, m, "Boom")
}

func TestServerPassFocusEmpty(t *testing.T) {
	m, f := passModel(t)
	f.passes, f.grantList = nil, nil
	m.Update(grantsMsg{server: "apollo"})
	press(m, "tab")
	if m.passFocus {
		t.Fatal("tab focused nothing")
	}
	if got := footer(m); strings.Contains(got, "tab") {
		t.Fatalf("footer %q", got)
	}
	m.selectRow(rowKey(rowServer, "mini"))
	f.passes = []engine.Passthrough{{PassthroughSpec: engine.PassthroughSpec{ID: "x", Server: "apollo", Remote: 1}}}
	press(m, "tab")
	if m.passFocus {
		t.Fatal("tab focused another server's passthroughs")
	}
}

func TestHomePassthroughCount(t *testing.T) {
	m, f := passModel(t)
	f.passes = append(f.passes, engine.Passthrough{PassthroughSpec: engine.PassthroughSpec{ID: "m1", Server: "mini", Requester: "codex", Remote: 3000}})
	m.selectRow(homeKey)
	if s := screenText(m); !strings.Contains(s, "passthroughs apollo 2 · mini 1") {
		t.Errorf("home lacks the count:\n%s", s)
	}
	f.passes = nil
	if s := screenText(m); strings.Contains(s, "passthroughs") {
		t.Errorf("home shows a count of none:\n%s", s)
	}
}

func TestPassFocusRowGone(t *testing.T) {
	m, f := passModel(t)
	press(m, "tab")
	press(m, "down")
	press(m, "down") // the grant
	f.passes = f.passes[:1]
	press(m, "up")
	if m.passRow != 0 || !m.passFocus {
		t.Fatalf("row %d, focus %v", m.passRow, m.passFocus)
	}
	f.passes = nil
	f.grantList = nil // the last rows go
	m.Update(grantsMsg{server: "apollo"})
	screen(m)
	if m.passFocus {
		t.Error("the focus stays with no rows")
	}
	if got := footer(m); strings.Contains(got, "close") {
		t.Errorf("footer %q", got)
	}
}

func TestPassSelectionFollowsItsPassthrough(t *testing.T) {
	m, f := passModel(t)
	pass := func(id string, remote int) engine.Passthrough {
		return engine.Passthrough{PassthroughSpec: engine.PassthroughSpec{ID: id, Server: "apollo", Requester: "codex", Remote: remote}, Local: remote}
	}
	f.passes = []engine.Passthrough{pass("p3000", 3000), pass("p4000", 4000)}
	press(m, "tab")
	press(m, "down") // 4000
	f.mu.Lock()
	f.passes = append(f.passes, pass("p2000", 2000)) // opened without a prompt
	f.mu.Unlock()
	run(m, press(m, "x"))
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"close p4000"}) {
		t.Fatalf("calls %v, want 4000's passthrough closed", got)
	}
}

func TestPassCloseIgnoredWhenSelectionGone(t *testing.T) {
	m, f := passModel(t)
	pass := func(id string, remote int) engine.Passthrough {
		return engine.Passthrough{PassthroughSpec: engine.PassthroughSpec{ID: id, Server: "apollo", Requester: "codex", Remote: remote}, Local: remote}
	}
	f.passes = []engine.Passthrough{pass("p3000", 3000), pass("p4000", 4000), pass("p5000", 5000)}
	press(m, "tab")
	press(m, "down") // 4000
	f.mu.Lock()
	f.passes = []engine.Passthrough{pass("p3000", 3000), pass("p5000", 5000)} // 4000 closed elsewhere
	f.mu.Unlock()
	run(m, press(m, "x"))
	if got := f.passthroughCalls(); len(got) != 0 {
		t.Fatalf("calls %v, want none: the row under the selection was never shown selected", got)
	}
	screen(m)
	run(m, press(m, "x"))
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"close p5000"}) {
		t.Fatalf("calls %v, want the row now shown selected closed", got)
	}
}

func TestServerPanelSilentWithoutAgents(t *testing.T) {
	m, f := passModel(t)
	f.passes, f.grantList, f.passErr = nil, nil, errBoom
	m.grantCache = nil
	run(m, m.fetchGrants()) // the grants cannot be read
	if s := screenText(m); strings.Contains(s, "always allowed") || strings.Contains(s, "Boom") {
		t.Errorf("an error with nothing to list:\n%s", s)
	}
	f.passes = []engine.Passthrough{{PassthroughSpec: engine.PassthroughSpec{ID: "x", Server: "apollo", Requester: "codex", Remote: 1}}}
	if s := screenText(m); !strings.Contains(s, "Boom") {
		t.Errorf("no error beside a passthrough:\n%s", s)
	}
}

// manyPasses gives apollo n passthroughs, on ports 3001 to 3000+n, and no
// grants, in a window low enough that only a few rows fit.
func manyPasses(t *testing.T, n int) (*Model, *fakeBackend) {
	t.Helper()
	m, f := passModel(t)
	f.mu.Lock()
	f.passes, f.grantList = nil, nil
	for i := 1; i <= n; i++ {
		f.passes = append(f.passes, engine.Passthrough{PassthroughSpec: engine.PassthroughSpec{
			ID: fmt.Sprintf("p%d", 3000+i), Server: "apollo", Requester: "codex", Remote: 3000 + i}, Local: 3000 + i})
	}
	f.mu.Unlock()
	m.Update(grantsMsg{server: "apollo"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 22})
	return m, f
}

// shownPorts are the ports of the passthrough rows on the screen, in order,
// and the screen's lines, with the index of each port's line.
func shownPorts(m *Model) ([]int, []string, map[int]int) {
	lines := strings.Split(screen(m), "\n")
	var ports []int
	at := map[int]int{}
	for i, l := range lines {
		var p, q int
		j := strings.Index(l, " → localhost:")
		if j < 0 {
			continue
		}
		k := strings.LastIndex(l[:j], " ")
		if _, err := fmt.Sscanf(l[k+1:j]+" "+l[j+len(" → localhost:"):], "%d %d", &p, &q); err == nil {
			ports = append(ports, p)
			at[p] = i
		}
	}
	return ports, lines, at
}

// lineEnds reports whether line i of lines, trimmed of the box and border
// cells around the panel, ends with want.
func lineEnds(lines []string, i int, want string) bool {
	return i >= 0 && i < len(lines) && strings.HasSuffix(strings.TrimSpace(strings.TrimRight(lines[i], "│█ ")), want)
}

func TestPassRowsScroll(t *testing.T) {
	m, _ := manyPasses(t, 12)
	press(m, "tab")
	ports, lines, at := shownPorts(m)
	if len(ports) < 3 || len(ports) >= 12 {
		t.Fatalf("shown %v:\n%s", ports, screen(m))
	}
	for i, p := range ports {
		if p != 3001+i {
			t.Fatalf("shown %v, want 3001 on:\n%s", ports, screen(m))
		}
	}
	last := ports[len(ports)-1]
	if want := fmt.Sprintf("+%d more", 12-len(ports)); !lineEnds(lines, at[last]+1, want) {
		t.Fatalf("no %q below the rows:\n%s", want, screen(m))
	}
	for i := range at[3001] {
		if lineEnds(lines, i, " more") {
			t.Fatalf("a marker above the first row:\n%s", screen(m))
		}
	}
	for range 8 {
		press(m, "down")
	}
	ports, lines, at = shownPorts(m)
	if !slices.Contains(ports, 3009) || slices.Contains(ports, 3001) {
		t.Fatalf("shown %v, want 3009 and not 3001:\n%s", ports, screen(m))
	}
	first := ports[0]
	if want := fmt.Sprintf("+%d more", first-3001); !lineEnds(lines, at[first]-2, want) && !lineEnds(lines, at[first]-1, want) {
		t.Fatalf("no %q above the rows:\n%s", want, screen(m))
	}
	m.panelErr = "the forward could not be closed"
	wantScreen(t, m, "The forward could not be closed")
	if ports, _, _ := shownPorts(m); !slices.Contains(ports, 3009) {
		t.Fatalf("shown %v with the error, want 3009:\n%s", ports, screen(m))
	}
}

func TestPassWindowKeepsSelectionAfterRemoval(t *testing.T) {
	m, f := manyPasses(t, 12)
	press(m, "tab")
	for range 11 {
		press(m, "down")
	}
	if ports, _, _ := shownPorts(m); !slices.Contains(ports, 3012) {
		t.Fatalf("shown %v, want 3012:\n%s", ports, screen(m))
	}
	f.mu.Lock()
	f.passes = append(f.passes[:4:4], f.passes[8:]...) // 3005 to 3008 close
	f.mu.Unlock()
	ports, _, _ := shownPorts(m)
	if !slices.Contains(ports, 3012) || m.passSel != "pass p3012" {
		t.Fatalf("shown %v, selected %q, want 3012:\n%s", ports, m.passSel, screen(m))
	}
	if len(ports) < 3 {
		t.Fatalf("shown %v, want at least 3 rows:\n%s", ports, screen(m))
	}
}

func TestRevokeBeatsStaleGrantsRead(t *testing.T) {
	m, f := passModel(t)
	m.grantCache["apollo"].at = time.Time{} // the grants are read again
	stale := m.fetchGrants()
	if stale == nil {
		t.Fatal("no grants read")
	}
	old := stale() // read before the revoke, with the grant
	press(m, "tab")
	press(m, "down")
	press(m, "down") // the grant
	press(m, "x")
	f.mu.Lock()
	f.grantList = nil
	f.mu.Unlock()
	settle(m, press(m, "y"))
	m.Update(old)
	if s := screenText(m); strings.Contains(s, "Claude Code · since") {
		t.Fatalf("the revoked grant came back:\n%s", s)
	}
}

func TestCloseInFlightIgnoresSecondX(t *testing.T) {
	m, f := passModel(t)
	press(m, "tab")
	first := press(m, "x")
	second := press(m, "x")
	run(m, first)
	run(m, second)
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"close ffff0000aaaa"}) {
		t.Fatalf("calls %v, want one close", got)
	}
	f.mu.Lock()
	f.passes = f.passes[1:]
	f.mu.Unlock()
	screen(m)
	run(m, press(m, "x")) // the close came back, so the next row closes
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"close ffff0000aaaa", "close id-not-shown"}) {
		t.Fatalf("calls %v", got)
	}
}

// A panel too short for three rows shows as many as fit, and the selected
// one is always among them. The longest error leaves room for the label and
// one row only.
func TestPassWindowOnAShortPanel(t *testing.T) {
	for _, words := range []int{0, 2, 4, 6, 8} {
		t.Run(strconv.Itoa(words), func(t *testing.T) {
			m, _ := manyPasses(t, 12)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
			press(m, "tab")
			for range 6 {
				press(m, "down")
			}
			// An error above the rows takes the room they would have.
			m.panelErr = strings.Repeat("the forward could not be closed ", words)
			if ports, _, _ := shownPorts(m); !slices.Contains(ports, 3007) {
				t.Fatalf("shown %v, want the selected 3007:\n%s", ports, screen(m))
			}
		})
	}
}
