package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// home is the Home panel.
func (m *Model) home(width int) []string {
	ca := m.caText()
	hostsDot, hostsText := styleBeacon.Render("●"), clean(m.hostsState)
	if _, ok := hostsWord(m.hostsState); !ok {
		hostsDot = styleAmber.Render("●")
	}
	if m.hostsState == hostsChecking {
		hostsDot, hostsText = styleDim.Render("●"), styleDim.Render("checking…")
	}
	running, serversUsed := 0, map[string]bool{}
	for _, p := range m.cfg.Projects {
		if m.b.Status(p.Name).State == engine.StateRunning {
			running++
			serversUsed[p.Server] = true
		}
	}
	lines := []string{styleTitle.Render("Nearhost")}
	lines = append(lines, m.proxyLines(width)...)
	lines = append(lines, m.webLines(width)...)
	lines = append(lines, field("local CA", ca, width)...)
	lines = append(lines, field("hosts", hostsDot+" "+hostsText, width)...)
	lines = append(lines, field("config", clean(m.b.ConfigPath()), width)...)
	lines = append(lines, field("running", fmt.Sprintf("%d of %d projects %s %d of %d servers", running, len(m.cfg.Projects),
		styleDim.Render("on"), len(serversUsed), len(m.cfg.Servers)+1), width)...)
	lines = append(lines, m.agentCounts(width)...)
	lines = append(lines, m.proposalsWaiting(width)...)
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

// webLines link to Nearhost's page, once the proxy serves it.
func (m *Model) webLines(width int) []string {
	port := m.b.WebPort()
	if port == 0 || !m.started || m.proxyErr != nil {
		return nil
	}
	url := fmt.Sprintf("https://localhost:%d", port)
	return field("page", styleBeacon.Underline(true).Hyperlink(url).Render(url), width)
}

// goos is the operating system, a var so tests can pick one.
var goos = runtime.GOOS

// executable is the path of the running binary, or "" if unknown; a var so
// tests can replace it.
var executable = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// proxyFix is what to do about the proxy being down, for the reason it is
// down: setcap on Linux when the port needs a privilege, the program using
// the port when it is taken. Caddy can return the error as text with the
// errno lost, so the message is the fallback. A file Nearhost could not
// create, such as its run directory, is no port problem.
func proxyFix(err error, goos, exe string) string {
	const ports = "pass --https-port and --http-port."
	var pe *fs.PathError
	if errors.As(err, &pe) {
		if errors.Is(pe.Err, fs.ErrPermission) {
			return "Check that this user can create " + pe.Path + "."
		}
		return "Fix " + pe.Path + ": " + pe.Err.Error() + "."
	}
	denied := errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "permission denied")
	if denied && goos == "linux" {
		return doctor.SetcapFix(exe)
	}
	if denied {
		return "Run nearhost as a user that may bind the port, or " + ports
	}
	return "Quit the program using the port, or " + ports
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
			fix: proxyFix(m.proxyErr, goos, executable())})
	}
	for _, srv := range m.cfg.Servers {
		h := m.b.ServerHealth(srv.Name)
		if h.Status == transport.StatusConnected && h.AgentErr != "" {
			// Not one for r on Home to retry: the server's projects serve,
			// and reconnecting would drop them for nothing.
			out = append(out, problem{dot: seg{styleAmber, "●"}, title: srv.Name, what: "agents unavailable",
				why: h.AgentErr, fix: agentFix(h.AgentProblem)})
		}
		if h.Status == transport.StatusConnected || h.Held || h.LastError == "" {
			continue // a server dropped on purpose is no problem
		}
		var on []string
		for _, p := range m.cfg.Projects {
			if p.Server == srv.Name {
				on = append(on, clean(p.Name))
			}
		}
		out = append(out, problem{dot: serverMark(h.Status), title: srv.Name, what: m.connectionText(h), why: h.LastError,
			fix:    serverFix(h.Status, srv.SSHHost),
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
			return tea.Batch(copyText(homeKey, "ssh "+p.host), m.alert(homeKey, "Copied ssh "+clean(p.host)+"."))
		}
	}
	return nil
}

// caWarnAhead is how long before the local CA expires Home warns of it.
const caWarnAhead = 30 * 24 * time.Hour

// caText says whether the local CA is trusted and when it expires.
func (m *Model) caText() string {
	ca := styleBeacon.Render("●") + " trusted"
	if !m.caTrusted {
		ca = styleAmber.Render("● not trusted") + ". Run nearhost trust"
	}
	if m.caExpires.IsZero() {
		return ca
	}
	date := m.caExpires.Format("2006-01-02")
	switch left := m.caExpires.Sub(m.now()); {
	case left <= 0:
		return styleRed.Render("● expired " + date)
	case left < caWarnAhead:
		return ca + styleAmber.Render(" · expires "+date)
	}
	return ca + styleDim.Render(" · expires "+date)
}

// connectionText says how a server's connection is: its state, how many
// attempts have failed, and when it tries next.
func (m *Model) connectionText(h engine.ServerHealth) string {
	if h.Held {
		return "dropped"
	}
	text := string(h.Status)
	switch h.Status {
	case transport.StatusAuthFailed:
		if h.Failures == 1 {
			text += " · tried once"
		} else if h.Failures > 1 {
			text += fmt.Sprintf(" · tried %d times", h.Failures)
		}
	case transport.StatusReconnecting:
		if h.Failures > 0 {
			text += fmt.Sprintf(" · attempt %d", h.Failures+1)
		}
		if !h.NextAttempt.IsZero() {
			if left := h.NextAttempt.Sub(m.now()); left > 0 {
				text += fmt.Sprintf(" · next in %ds", int(math.Ceil(left.Seconds())))
			} else {
				text += " · trying now"
			}
		}
	}
	return text
}

// serverFix says what to do about a server in state st. A server that gave
// up waits for r; one that is reconnecting tries again by itself.
func serverFix(st transport.Status, host string) string {
	if st == transport.StatusReconnecting {
		return "Nearhost tries again by itself. If it keeps failing, run ssh " + clean(host) + " once in a terminal to check access."
	}
	return "Run ssh " + clean(host) + " once in a terminal to fix access, then press r."
}
