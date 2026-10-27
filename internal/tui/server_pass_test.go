package tui

import (
	"slices"
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
		{PassthroughSpec: engine.PassthroughSpec{ID: "1a2b3c4d5e6f7a8b", Server: "apollo", Requester: "claude-code", Label: "browser preview", Remote: 41873},
			Local: 41873, Opened: m.now().Add(-12 * time.Minute)},
	}
	f.grantList = []grants.Grant{{Server: "apollo", Requester: "claude-code", Kind: grants.KindPassthrough,
		Created: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)}}
	m.selectRow(rowKey(rowServer, "apollo"))
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
		"5173 → localhost:5173 · Codex · ffff0000 · 2h",
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
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"close 1a2b3c4d5e6f7a8b"}) {
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
	want := []string{"close 1a2b3c4d5e6f7a8b", "revoke apollo claude-code"}
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
	screen(m)
	if m.passFocus {
		t.Error("the focus stays with no rows")
	}
	if got := footer(m); strings.Contains(got, "close") {
		t.Errorf("footer %q", got)
	}
}
