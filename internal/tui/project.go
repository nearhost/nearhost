package tui

import (
	"fmt"
	"strconv"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// projectPanel is the panel of the project called name.
func (m *Model) projectPanel(name string, width int) []string {
	p, ok := m.cfg.Project(name)
	if !ok {
		return nil
	}
	st := m.b.Status(name)
	server := "this machine"
	host := ""
	if p.Server != config.LocalServer {
		srv, _ := m.cfg.Server(p.Server)
		host = srv.SSHHost
		server = clean(p.Server) + styleDim.Render(" (ssh "+clean(host)+")")
	}
	state := stateDot(st.State) + " " + string(st.State)
	if !p.Enabled {
		state += styleDim.Render(" · disabled")
	}
	lines := []string{
		styleTitle.Render(clean(p.Name)),
		styleBeacon.Render("https://"+clean(p.Alias)) + styleDim.Render("  o to open"),
		"",
	}
	lines = append(lines, field("server", server, width)...)
	lines = append(lines, field("path", clean(p.Path), width)...)
	lines = append(lines, field("state", state, width)...)
	if st.Message != "" {
		lines = append(lines, field("", clean(st.Message), width)...)
	}
	serverUp := true
	if p.Server != config.LocalServer {
		status, _ := m.b.ServerStatus(p.Server)
		serverUp = status == transport.StatusConnected
	}
	if fix := fixFor(st.State, st.Message, serverUp, host); fix != "" {
		lines = append(lines, field("fix", fix, width)...)
	}
	// With the focus on the routes, a mark shows the selected one.
	lead := func(i int) string {
		switch {
		case !m.routeFocus:
			return ""
		case i == m.routeRow:
			return styleBeacon.Render("▸ ")
		}
		return "  "
	}
	lines = append(lines, "", lead(-1)+styleDim.Render(fmt.Sprintf("%-6s %-9s %-24s %s", "PORT", "ROLE", "LABEL", "LOCAL")))
	for i, r := range p.Routes {
		lines = append(lines, lead(i)+fmt.Sprintf("%-6d %-9s %-24s %s", r.Port, r.Role, fit(clean(r.Label), 24), localPort(*p, r, st)))
	}
	if m.panelErr != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	return append(lines, m.adviceLinesFor(name, width)...)
}

// localPort is where a route can be reached on this machine, or "" when it
// is not forwarded.
func localPort(p config.Project, r config.Route, st engine.ProjectStatus) string {
	switch r.Role {
	case config.RoleWeb:
		if st.WebLocalPort != 0 {
			return ":" + strconv.Itoa(st.WebLocalPort)
		}
	case config.RoleLoopback:
		if p.Server != config.LocalServer {
			return ":" + strconv.Itoa(r.Port)
		}
	}
	return ""
}

// fixFor says what to do about a project in state st with reason, as up
// says it, for someone at the interface. serverUp reports whether the
// project's server is connected; host is its ssh_host.
func fixFor(st engine.State, reason string, serverUp bool, host string) string {
	switch st {
	case engine.StateDegraded:
		return "None needed while Nearhost reconnects. If it stays degraded, run `ssh " + clean(host) + "` once in a terminal to fix access."
	case engine.StateStopped:
		// A stopped project with a reason was refused its hosts entry.
		if reason != "" {
			return "Press enter and approve the password prompt, or add the alias to /etc/hosts yourself and run nearhost with --no-hosts."
		}
	case engine.StateError:
		if !serverUp {
			return "Run `ssh " + clean(host) + "` once in a terminal to fix access, then press enter."
		}
		return "Resolve what is named above, then press enter to start it again."
	}
	return ""
}
