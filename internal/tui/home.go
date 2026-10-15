package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// markLines draw the Nearhost mark: an arch over a dot.
var markLines = []string{
	styleBeacon.Render("╭───╮"),
	styleBeacon.Render("│") + "   " + styleBeacon.Render("│"),
	styleBeacon.Render("│") + " ● " + styleBeacon.Render("│"),
}

// home is the Home panel. With no projects it is the empty state, below the
// proxy's trouble when there is any.
func (m *Model) home(width int) []string {
	if len(m.cfg.Projects) == 0 {
		var lines []string
		if m.proxyErr != nil {
			lines = append(append(m.proxyLines(width), m.problemLines(width)...), "")
		}
		lines = append(lines, markLines...)
		return append(lines, "",
			styleTitle.Render("Nothing to serve yet."),
			"Press n to add the project in the current directory.")
	}
	ca := styleBeacon.Render("●") + " trusted"
	if !m.caTrusted {
		ca = styleAmber.Render("● not trusted") + ". Run nearhost trust"
	}
	hostsDot := styleBeacon.Render("●")
	if _, ok := hostsWord(m.hostsState); !ok {
		hostsDot = styleAmber.Render("●")
	}
	running, serversUsed := 0, map[string]bool{}
	for _, p := range m.cfg.Projects {
		if m.b.Status(p.Name).State == engine.StateRunning {
			running++
			serversUsed[p.Server] = true
		}
	}
	lines := []string{styleTitle.Render("Nearhost"), ""}
	lines = append(lines, m.proxyLines(width)...)
	lines = append(lines, field("local CA", ca, width)...)
	lines = append(lines, field("hosts", hostsDot+" "+clean(m.hostsState), width)...)
	lines = append(lines, field("config", clean(m.b.ConfigPath()), width)...)
	lines = append(lines, field("running", fmt.Sprintf("%d of %d projects %s %d of %d servers", running, len(m.cfg.Projects),
		styleDim.Render("on"), len(serversUsed), len(m.cfg.Servers)+1), width)...)
	return append(lines, m.problemLines(width)...)
}

// proxyLines say whether the proxy is up.
func (m *Model) proxyLines(width int) []string {
	https, http := m.b.Ports()
	switch {
	case m.proxyErr != nil:
		return field("proxy", styleRed.Render("● down"), width)
	case !m.started:
		return field("proxy", styleAmber.Render("◐ starting"), width)
	}
	return field("proxy", styleBeacon.Render("●")+fmt.Sprintf(" https :%d · http :%d", https, http), width)
}

// problem is something that needs the user: what it is, why, what to do,
// and what it stops.
type problem struct {
	dot          seg
	title, what  string
	why, fix     string
	blocks       []string
	server, host string // the server it is about, which r and y act on
}

// problems are what needs the user: the proxy being down, servers that
// cannot connect, and projects in error.
func (m *Model) problems() []problem {
	var out []problem
	if m.proxyErr != nil {
		out = append(out, problem{dot: seg{styleRed, "●"}, title: "proxy", what: "down", why: m.proxyErr.Error(),
			fix: "Quit the program using the port, or run nearhost with --https-port and --http-port."})
	}
	for _, srv := range m.cfg.Servers {
		st, lastErr := m.b.ServerStatus(srv.Name)
		if st == transport.StatusConnected || lastErr == "" {
			continue
		}
		var on []string
		for _, p := range m.cfg.Projects {
			if p.Server == srv.Name {
				on = append(on, clean(p.Name))
			}
		}
		out = append(out, problem{dot: serverMark(st), title: srv.Name, what: string(st), why: lastErr,
			fix:    "Run ssh " + clean(srv.SSHHost) + " once in a terminal to fix access, then press r.",
			blocks: on, server: srv.Name, host: srv.SSHHost})
	}
	for _, p := range m.cfg.Projects {
		st := m.b.Status(p.Name)
		if st.State != engine.StateError {
			continue
		}
		serverUp, host := true, ""
		if p.Server != config.LocalServer {
			srv, _ := m.cfg.Server(p.Server)
			status, _ := m.b.ServerStatus(p.Server)
			serverUp, host = status == transport.StatusConnected, srv.SSHHost
		}
		out = append(out, problem{dot: seg{styleRed, "●"}, title: p.Name, what: "error", why: st.Message,
			fix: fixFor(st.State, st.Message, serverUp, host)})
	}
	return out
}

// problemLines draw the problems under a heading, with the keys that act on
// servers that cannot connect.
func (m *Model) problemLines(width int) []string {
	problems := m.problems()
	if len(problems) == 0 {
		return nil
	}
	lines := []string{"", section("problems", "", width)}
	servers := false
	for _, p := range problems {
		lines = append(lines, "", p.dot.String()+" "+styleTitle.Render(clean(p.title))+"  "+p.dot.style.Render(p.what))
		indent := func(label, value string) {
			for _, l := range field(label, value, width-2) {
				lines = append(lines, "  "+l)
			}
		}
		if p.why != "" {
			indent("why", clean(p.why))
		}
		if p.fix != "" {
			indent("fix", p.fix)
		}
		if len(p.blocks) > 0 {
			indent("blocks", strings.Join(p.blocks, ", "))
		}
		servers = servers || p.server != ""
	}
	if servers {
		lines = append(lines, "", "  "+styleBeacon.Render("r")+styleDim.Render(" retry now · ")+styleBeacon.Render("y")+styleDim.Render(" copy command"))
	}
	return lines
}

// retryServers reconnects every server that cannot connect.
func (m *Model) retryServers() tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range m.problems() {
		if p.server != "" {
			cmds = append(cmds, m.reconnect(p.server))
		}
	}
	return tea.Batch(cmds...)
}

// copySSH puts the command that fixes access to the first server that
// cannot connect on the clipboard.
func (m *Model) copySSH() tea.Cmd {
	for _, p := range m.problems() {
		if p.host != "" {
			m.logs.noteOn(homeKey, "Copied ssh "+clean(p.host)+".")
			return tea.SetClipboard("ssh " + p.host)
		}
	}
	return nil
}
