package tui

import (
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// serverPanel is the panel of the server called name, in height lines
// where it can be.
func (m *Model) serverPanel(name string, width, height int) []string {
	if name == config.LocalServer {
		lines := []string{
			styleTitle.Render("local"),
			"",
			"Projects on this machine. There is nothing to connect to.",
		}
		lines = append(lines, m.openField(name, width)...)
		if m.panelErr != "" {
			lines = append(lines, "")
			lines = append(lines, errorLines(m.panelErr, width)...)
		}
		if rows := m.passLines(name, width, height-len(lines)-1); len(rows) > 0 {
			lines = append(append(lines, ""), rows...)
		}
		return lines
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
		// A server reconnecting tries again by itself; one that gave up
		// waits for r, unless its run directory is the trouble.
		msg := serverProblem(h, srv)
		var hint footerItem
		if st != transport.StatusReconnecting && reconnectHelps(msg) {
			hint = footerItem{"r", "reconnect"}
		}
		lines = append(lines, problemFields(msg, hint, width)...)
	}
	if st == transport.StatusConnected {
		switch {
		case h.AgentPath != "":
			lines = append(lines, field("agents", glyph(markRunning).String()+" reach Nearhost at "+clean(h.AgentPath), width)...)
		case h.AgentErr != "":
			lines = append(lines, field("agents", glyph(markAttention).String()+styleAmber.Render(" unavailable")+" "+clean(h.AgentErr), width)...)
			msg := agentProblem(h)
			var hint footerItem
			if msg.Code == report.AgentsNotReady || msg.Code == report.AgentsRefused {
				hint = footerItem{"r", "reconnect"}
			}
			lines = append(lines, fixFields(msg, hint, width)...)
		}
	}
	projects := "none"
	if len(on) > 0 {
		projects = strings.Join(on, ", ")
	}
	lines = append(lines, field("projects", projects, width)...)
	lines = append(lines, m.openField(name, width)...)
	// The error comes before the grants, so a long list of them cannot push
	// it out of the panel.
	if m.panelErr != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	if rows := m.passLines(name, width, height-len(lines)-1); len(rows) > 0 {
		lines = append(append(lines, ""), rows...)
	}
	return lines
}

// openField is the open field of the server's panel, or nothing while no
// agent has anything open on it.
func (m *Model) openField(name string, width int) []string {
	if s := m.openCounts(name); s != "" {
		return field("open", s, width)
	}
	return nil
}
