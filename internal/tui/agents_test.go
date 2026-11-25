package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// With nothing open, each agents row's panel shows its title and what
// would put something there.
func TestAgentsPanelEmpty(t *testing.T) {
	for _, c := range []struct{ key, title, empty string }{
		{"passthroughs", "passthroughs", "No ports are open for agents. When an agent asks to open one, Nearhost asks you first."},
		{"documents", "documents", "No documents are being served. When an agent writes a plan or report for you, it can ask to open it here."},
	} {
		m := sized(newFake(t, sampleConfig()))
		m.selectRow(c.key)
		lines := m.agentsPanel(map[string]rowKind{"passthroughs": rowPasses, "documents": rowDocs}[c.key], 60, 10)
		if len(lines) == 0 || !strings.Contains(lines[0], c.title) {
			t.Errorf("%s: title line %q", c.key, lines)
		}
		if got := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, " "))), " "); !strings.Contains(got, c.empty) {
			t.Errorf("%s: panel lacks the empty state: %q", c.key, got)
		}
		if got := screen(m); !strings.Contains(got, c.title) || !strings.Contains(got, "ask") {
			t.Errorf("%s: screen lacks the panel:\n%s", c.key, got)
		}
	}
}

// panelModel has leto and apollo in the config, in that order, with a
// passthrough on each; the passthroughs row is selected.
func panelModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	cfg := sampleConfig()
	cfg.Servers = []config.Server{{Name: "leto", SSHHost: "leto.lan"}, {Name: "apollo", SSHHost: "apollo.lan"}}
	f := newFake(t, cfg)
	m := sized(f)
	f.passes = []engine.Passthrough{
		{PassthroughSpec: engine.PassthroughSpec{ID: "apollo-1", Server: "apollo", Requester: "codex", Label: "storybook", Remote: 6006},
			Local: 5174, Opened: m.now().Add(-2 * time.Hour)},
		{PassthroughSpec: engine.PassthroughSpec{ID: "leto-1", Server: "leto", Requester: "claude-code", Label: "preflash demo", Remote: 7480},
			Local: 7480, Opened: m.now().Add(-12 * time.Minute)},
	}
	m.selectRow("passthroughs")
	return m, f
}

func panelText(m *Model) []string {
	var out []string
	for _, l := range m.agentsPanel(rowPasses, 60, 20) {
		out = append(out, strings.TrimRight(ansi.Strip(l), " "))
	}
	return out
}

func TestPassPanelListsEveryServer(t *testing.T) {
	m, _ := panelModel(t)
	got := panelText(m)
	want := []string{
		"http://localhost:7480   preflash demo",
		"from leto, port 7480 · Claude Code · 12m",
		"http://localhost:5174   storybook",
		"from apollo, port 6006 · Codex · 2h",
	}
	if !strings.HasSuffix(got[0], "2 open") || !strings.HasPrefix(got[0], "passthroughs") {
		t.Errorf("title line %q", got[0])
	}
	var rest []string
	for _, l := range got[1:] {
		if strings.TrimSpace(l) != "" {
			rest = append(rest, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "▌")))
		}
	}
	if !slices.Equal(rest, want) {
		t.Errorf("lines %q, want %q", rest, want)
	}
}

func TestPassPanelUnknownServerLast(t *testing.T) {
	m, f := panelModel(t)
	f.passes = append([]engine.Passthrough{{PassthroughSpec: engine.PassthroughSpec{ID: "gone-1", Server: "gone", Requester: "codex", Remote: 3000},
		Local: 3000, Opened: m.now()}}, f.passes...)
	got := strings.Join(panelText(m), "\n")
	if i, j := strings.Index(got, "from apollo"), strings.Index(got, "from gone, port 3000"); i < 0 || j < i {
		t.Errorf("gone is not listed last:\n%s", got)
	}
}

func TestPassPanelKeys(t *testing.T) {
	m, f := panelModel(t)
	press(m, "tab")
	if !m.passFocus || m.passRow != 0 {
		t.Fatalf("tab did not focus the first row: %v %d", m.passFocus, m.passRow)
	}
	if got := footer(m); got != "↑↓ select · o open · y copy · x close · esc done" {
		t.Errorf("footer %q", got)
	}
	press(m, "down")
	if m.passRow != 1 {
		t.Fatalf("down: row %d", m.passRow)
	}
	run(m, press(m, "o"))
	if !slices.Equal(f.opened, []string{"http://localhost:5174"}) {
		t.Errorf("opened %q", f.opened)
	}
	if got := clipboardOf(press(m, "y")); got != "http://localhost:5174" {
		t.Errorf("copied %q", got)
	}
	run(m, press(m, "x"))
	if !slices.Equal(f.passCalls, []string{"close apollo-1"}) {
		t.Errorf("calls %q", f.passCalls)
	}
	press(m, "esc")
	if m.passFocus {
		t.Error("esc kept the focus")
	}
}

func TestPassPanelCloseFollowsSelection(t *testing.T) {
	m, f := panelModel(t)
	f.passes = append(f.passes, engine.Passthrough{PassthroughSpec: engine.PassthroughSpec{ID: "apollo-2", Server: "apollo", Requester: "codex", Remote: 9000},
		Local: 9000, Opened: m.now()})
	press(m, "tab")
	press(m, "down") // apollo's 6006, after leto's
	m.agentsPanel(rowPasses, 60, 20)
	f.passes = slices.DeleteFunc(f.passes, func(p engine.Passthrough) bool { return p.ID == "apollo-1" }) // closed elsewhere
	run(m, press(m, "x"))
	if len(f.passCalls) != 0 {
		t.Errorf("x closed %q before a redraw", f.passCalls)
	}
	m.agentsPanel(rowPasses, 60, 20)
	run(m, press(m, "x"))
	if !slices.Equal(f.passCalls, []string{"close apollo-2"}) {
		t.Errorf("calls %q", f.passCalls)
	}
}

func TestPassPanelSessionShownWhenAmbiguous(t *testing.T) {
	m, f := panelModel(t)
	if got := strings.Join(panelText(m), "\n"); strings.Contains(got, "1a2b3c4d") {
		t.Errorf("a lone row shows its session:\n%s", got)
	}
	f.passes = []engine.Passthrough{
		{PassthroughSpec: engine.PassthroughSpec{ID: "a", Server: "leto", Requester: "codex", Session: "1a2b3c4d5e6f7a8b", Remote: 7000}, Local: 7000, Opened: m.now()},
		{PassthroughSpec: engine.PassthroughSpec{ID: "b", Server: "leto", Requester: "codex", Session: "99887766aabbccdd", Remote: 7001}, Local: 7001, Opened: m.now()},
	}
	got := strings.Join(panelText(m), "\n")
	if !strings.Contains(got, "Codex · 1a2b3c4d · <1m") || !strings.Contains(got, "Codex · 99887766 · <1m") {
		t.Errorf("sessions not shown:\n%s", got)
	}
}
