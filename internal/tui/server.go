package tui

import (
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

// serverPanel is the panel of the server called name.
func (m *Model) serverPanel(name string, width int) []string {
	if name == config.LocalServer {
		return []string{
			styleTitle.Render("local"),
			"",
			"Projects on this machine. There is nothing to connect to.",
		}
	}
	srv, ok := m.cfg.Server(name)
	if !ok {
		return nil
	}
	st, lastErr := m.b.ServerStatus(name)
	var on []string
	for _, p := range m.cfg.Projects {
		if p.Server == name {
			on = append(on, clean(p.Name))
		}
	}
	state := m.serverDot(st).String() + " " + string(st)
	lines := []string{spread(styleTitle.Render(clean(name)), state, width), ""}
	lines = append(lines, field("ssh host", clean(srv.SSHHost), width)...)
	if lastErr != "" {
		lines = append(lines, field("why", clean(lastErr), width)...)
		if st != transport.StatusConnected {
			lines = append(lines, field("fix", "Run ssh "+clean(srv.SSHHost)+" once in a terminal to fix access, then press r.", width)...)
		}
	}
	projects := "none"
	if len(on) > 0 {
		projects = strings.Join(on, ", ")
	}
	lines = append(lines, field("projects", projects, width)...)
	if m.panelErr != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	return lines
}
