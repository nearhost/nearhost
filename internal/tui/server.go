package tui

import (
	"strconv"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

// serverPanel is the panel of the server called name.
func (m *Model) serverPanel(name string, width int) []string {
	if name == config.LocalServer {
		return []string{
			styleTitle.Render("Local"),
			"",
			"Projects on this machine. There is nothing to connect to.",
		}
	}
	srv, ok := m.cfg.Server(name)
	if !ok {
		return nil
	}
	st, lastErr := m.b.ServerStatus(name)
	projects := 0
	for _, p := range m.cfg.Projects {
		if p.Server == name {
			projects++
		}
	}
	lines := []string{styleTitle.Render(clean(name)), ""}
	lines = append(lines, field("ssh host", clean(srv.SSHHost), width)...)
	lines = append(lines, field("state", m.serverDot(st).String()+" "+string(st), width)...)
	if lastErr != "" {
		lines = append(lines, field("error", clean(lastErr), width)...)
		if st != transport.StatusConnected {
			lines = append(lines, field("fix", "Run `ssh "+clean(srv.SSHHost)+"` once in a terminal to fix access.", width)...)
		}
	}
	lines = append(lines, field("projects", strconv.Itoa(projects), width)...)
	if m.panelErr != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	return lines
}
