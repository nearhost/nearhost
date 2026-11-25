package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
	"github.com/nearhost/nearhost/internal/tui"
)

// A server whose run directory is too long for ssh keeps that problem's code
// from the core, through the control API's state and the client's mirror,
// to Home: which then offers neither r nor the ssh command, as it would for
// a login refused.
func TestSocketPathProblemSurvivesTheMirror(t *testing.T) {
	h := newMirrorHarness(t)
	data, err := config.Marshal(&config.Config{Servers: []config.Server{{Name: "vps", SSHHost: "deploy@vps.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	const reason = "control socket path /very/long/run/vps.sock is too long for ssh: it is 120 bytes and at most 86 bytes fit"
	problem := report.Errorf(report.SSHSocketPathTooLong, "%s", reason).Then(transport.SocketPathFix)
	h.core.set(func(st *control.State) {
		st.Config = string(data)
		st.Projects = map[string]engine.ProjectStatus{}
		st.Servers["vps"] = control.ServerState{
			Health: engine.ServerHealth{Health: transport.Health{Status: transport.StatusAuthFailed, LastError: reason, Problem: problem}},
			Status: transport.StatusAuthFailed, Message: reason, Problem: problem,
		}
	})
	b := h.backend(t, h.state(t), nil, nil)
	got := b.ServerHealth("vps").Problem
	if got == nil || got.Code != report.SSHSocketPathTooLong || len(got.Fix) != 1 || got.Fix[0].Text != transport.SocketPathFix {
		t.Fatalf("the mirror's problem = %+v, want %s with its step", got, report.SSHSocketPathTooLong)
	}
	var m tea.Model = tui.New(b, t.TempDir())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	screen := ansi.Strip(m.View().Content)
	text := strings.Join(strings.Fields(screen), " ")
	if !strings.Contains(text, "Set XDG_CONFIG_HOME to a shorter path") {
		t.Errorf("Home lacks the problem's step:\n%s", screen)
	}
	for _, not := range []string{"r retry", "copy command", "ssh deploy@vps.example"} {
		if strings.Contains(screen, not) {
			t.Errorf("Home offers %q:\n%s", not, screen)
		}
	}
}
