package tui

import (
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

// agentFix is what to do when agents on a server cannot reach Nearhost,
// for each kind of problem.
func agentFix(p transport.AgentProblem) string {
	switch p {
	case transport.AgentNotReady:
		return "Check that the server account can write ~/.nearhost, then press r on the server to reconnect."
	case transport.AgentTooLong:
		return "The server account's home directory is too long for the socket, so agents there cannot reach Nearhost."
	}
	return "Set AllowStreamLocalForwarding yes in the server's sshd_config, then press r on the server to reconnect."
}

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
	h := m.b.ServerHealth(name)
	st, lastErr := h.Status, h.LastError
	var on []string
	for _, p := range m.cfg.Projects {
		if p.Server == name {
			on = append(on, clean(p.Name))
		}
	}
	state := m.serverDot(st).String() + " " + m.connectionText(h)
	lines := []string{spread(styleTitle.Render(clean(name)), state, width), ""}
	lines = append(lines, field("ssh host", clean(srv.SSHHost), width)...)
	// The last error stays after the server connects again; it is only
	// news while the server is not connected.
	if h.Held {
		lines = append(lines, field("why", "you dropped the connection; its projects wait", width)...)
		lines = append(lines, field("fix", "Press c to connect it.", width)...)
	} else if lastErr != "" && st != transport.StatusConnected {
		lines = append(lines, field("why", clean(lastErr), width)...)
		lines = append(lines, field("fix", serverFix(st, srv.SSHHost), width)...)
	}
	if st == transport.StatusConnected {
		switch {
		case h.AgentPath != "":
			lines = append(lines, field("agents", styleBeacon.Render("●")+" reach Nearhost at "+clean(h.AgentPath), width)...)
		case h.AgentErr != "":
			lines = append(lines, field("agents", styleAmber.Render("● unavailable")+" "+clean(h.AgentErr), width)...)
			lines = append(lines, field("fix", agentFix(h.AgentProblem), width)...)
		}
	}
	projects := "none"
	if len(on) > 0 {
		projects = strings.Join(on, ", ")
	}
	lines = append(lines, field("projects", projects, width)...)
	if rows := m.passLines(name, width); len(rows) > 0 {
		lines = append(append(lines, ""), rows...)
	}
	if m.panelErr != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	return lines
}
