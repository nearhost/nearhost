package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/transport"
)

// projectPanel is the panel of the project called name.
func (m *Model) projectPanel(name string, width int) []string {
	p, ok := m.cfg.Project(name)
	if !ok {
		return nil
	}
	st := m.b.Status(name)
	host := ""
	server := "this machine"
	if p.Server != config.LocalServer {
		srv, _ := m.cfg.Server(p.Server)
		host, server = srv.SSHHost, clean(p.Server)
	}
	url := "https://" + clean(p.Alias)
	title := styleTitle.Render(clean(p.Name)) + "  " + styleBeacon.Underline(true).Hyperlink(url).Render(url)
	state := m.stateDot(st.State).String() + " " + string(st.State)
	if up := m.uptime(name); up != "" {
		state += styleDim.Render(" · up " + up)
	}
	if !p.Enabled {
		state += styleDim.Render(" · disabled")
	}
	sep := styleDim.Render(" · ")
	var lines []string
	if m.narrow() {
		lines = []string{title, state + sep + server + sep + clean(p.Path)}
	} else {
		meta := styleDim.Render("server") + " " + server + "    " + styleDim.Render("path") + " " + clean(p.Path)
		if presets := m.presetNames(name); presets != "" {
			meta = styleDim.Render("server") + " " + server + "    " + styleDim.Render("preset") + " " + presets +
				"    " + styleDim.Render("path") + " " + clean(p.Path)
		}
		lines = []string{spread(title, state, width), fit(meta, width)}
	}
	if urls := otherURLs(*p, width); urls != "" {
		lines = append(lines, urls)
	}
	if st.Message != "" {
		lines = append(lines, field("why", clean(st.Message), width)...)
	}
	serverUp := true
	if p.Server != config.LocalServer {
		status, _ := m.b.ServerStatus(p.Server)
		serverUp = status == transport.StatusConnected
	}
	if fix := fixFor(st.State, st.Message, serverUp, host); fix != "" {
		lines = append(lines, field("fix", fix, width)...)
	}
	if more := pendingLines(st, width); len(more) > 0 {
		lines = append(append(lines, m.gap()...), more...)
	}
	lines = append(lines, m.gap()...)
	lines = append(lines, m.routeTable(*p, st, width)...)
	if m.panelErr != "" {
		lines = append(lines, m.gap()...)
		lines = append(lines, errorLines(m.panelErr, width)...)
	}
	return append(lines, m.adviceLinesFor(name, width)...)
}

// otherURLs is one line with every URL of the project but the alias, which
// the title shows: a host is a link and a pattern, which no browser can open,
// is dim. It is cut to width rather than wrapped, and "" when there are none.
func otherURLs(p config.Project, width int) string {
	var parts []string
	for _, w := range p.Web() {
		for _, h := range w.Hosts {
			if strings.EqualFold(h, p.Alias) {
				continue
			}
			url := "https://" + clean(h)
			if hostname.IsPattern(h) {
				parts = append(parts, styleDim.Render(url))
			} else {
				parts = append(parts, styleBeacon.Underline(true).Hyperlink(url).Render(url))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fit(strings.Join(parts, "  "), width)
}

// presetNames names the presets that apply to a project, without their
// registries, as its advice last said; "" when none do or it is not known.
func (m *Model) presetNames(name string) string {
	var names []string
	for _, key := range m.advice[name].presets {
		_, plain := config.SplitPreset(key)
		names = append(names, clean(plain))
	}
	return strings.Join(names, ", ")
}

// routeColumns are the widths of the routes table's columns; label takes
// what is left, and the compact layout has no label.
func (m *Model) routeColumns(width int) (port, role, label, local int) {
	if m.narrow() {
		return 7, 12, 0, 8
	}
	return 8, 12, max(width-8-12-10-7, 8), 10
}

// routeTable draws a project's routes: a header, then a row each. With the
// focus on the routes, the selected one is filled. Ignored routes fold into
// one line after the rest unless i shows them, or the routes have the keys,
// so every route can be selected.
func (m *Model) routeTable(p config.Project, st engine.ProjectStatus, width int) []string {
	fold := !m.showIgnored && !m.routeFocus
	var ignored []string
	port, role, label, local := m.routeColumns(width)
	head := fit("port", port) + fit("role", role)
	if label > 0 {
		head += fit("label", label)
	}
	head += fit("local", local) + "status"
	lines := []string{" " + styleDim.Render(fit(head, width-1))}
	for i, r := range p.Routes {
		if r.Role == config.RoleIgnore {
			ignored = append(ignored, strconv.Itoa(r.Port))
			if fold {
				continue
			}
		}
		plain, roleStyle := styleText, styleText
		switch r.Role {
		case config.RoleWeb:
			roleStyle = styleBeacon
		case config.RoleIgnore:
			plain, roleStyle = styleDim, styleDim
		}
		where := localPort(p, r, st)
		if where == "" {
			where = "–"
		}
		cells := []seg{{plain, fit(strconv.Itoa(r.Port), port)}, {roleStyle, fit(string(r.Role), role)}}
		if label > 0 {
			cells = append(cells, seg{plain, fit(clean(r.Label), label-1) + " "})
		}
		cells = append(cells, seg{plain, fit(where, local)}, routeDot(r, st))
		lines = append(lines, renderRow(cells, nil, width, m.routeFocus && i == m.routeRow))
	}
	switch {
	case len(ignored) == 0 || m.routeFocus:
	case fold:
		lines = append(lines, " "+fit(styleDim.Render(fmt.Sprintf("%d ignored: %s", len(ignored), strings.Join(ignored, " ")))+
			styleDim.Render("  ·  ")+styleBeacon.Render("i")+styleDim.Render(" show"), width-1))
	default:
		lines = append(lines, " "+fit(styleBeacon.Render("i")+styleDim.Render(fmt.Sprintf(" hide the %d ignored", len(ignored))), width-1))
	}
	return lines
}

// routeDot says whether a route is served: the web route and the loopback
// routes are while the project runs; an ignored one never is.
func routeDot(r config.Route, st engine.ProjectStatus) seg {
	switch {
	case r.Role == config.RoleIgnore:
		return seg{styleDim, "–"}
	case st.State == engine.StateRunning:
		return seg{styleBeacon, "●"}
	case st.State == engine.StateStarting, st.State == engine.StateDegraded:
		return seg{styleAmber, "◐"}
	}
	return seg{styleDim, "○"}
}

// uptime is how long a project has been running, or "" when it is not.
func (m *Model) uptime(name string) string {
	since, ok := m.since[name]
	if !ok {
		return ""
	}
	return formatUptime(m.now().Sub(since))
}

// formatUptime says how long d is in a few cells: 45s, 14m, 2h 14m, 3d 2h.
func formatUptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// trackUptime notes when each project started running, and forgets it when
// a project stops.
func (m *Model) trackUptime() {
	for _, p := range m.cfg.Projects {
		running := m.b.Status(p.Name).State == engine.StateRunning
		_, known := m.since[p.Name]
		switch {
		case running && !known:
			m.since[p.Name] = m.now()
		case !running && known:
			delete(m.since, p.Name)
		}
	}
}

// localPort is where a route can be reached on this machine, or "" when it
// is not forwarded. Each web route has a local port of its own; a status
// without WebLocalPorts only knows the primary's, so only the primary
// shows it.
func localPort(p config.Project, r config.Route, st engine.ProjectStatus) string {
	switch r.Role {
	case config.RoleWeb:
		local := st.WebLocalPorts[r.Port]
		if st.WebLocalPorts == nil {
			if primary, _ := p.WebRoute(); primary.Port == r.Port {
				local = st.WebLocalPort
			}
		}
		if local != 0 {
			return strconv.Itoa(local)
		}
	case config.RoleLoopback:
		if p.Server != config.LocalServer {
			return strconv.Itoa(r.Port)
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
			return "Press enter and approve the password prompt, or add the project's hosts to /etc/hosts yourself and run nearhost with --no-hosts."
		}
	case engine.StateError:
		if !serverUp {
			return "Run `ssh " + clean(host) + "` once in a terminal to fix access, then press enter."
		}
		return "Resolve what is named above, then press enter to start it again."
	}
	return ""
}
