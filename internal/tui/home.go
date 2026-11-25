package tui

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// home is the Home panel.
func (m *Model) home(width int) []string {
	ca := m.caText()
	hostsDot, hostsText := glyph(markRunning).String(), clean(m.hostsState)
	if _, ok := hostsWord(m.hostsState); !ok {
		hostsDot = glyph(markAttention).String()
	}
	if m.hostsState == hostsChecking {
		hostsDot, hostsText = glyph(markStopped).String(), styleDim.Render("checking…")
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
		return field("proxy", glyph(markError).String()+styleRed.Render(" down"), width)
	case !m.started:
		return field("proxy", busyMark().String()+styleAmber.Render(" starting"), width)
	}
	return field("proxy", glyph(markRunning).String()+fmt.Sprintf(" https :%d · http :%d", https, http), width)
}

// pageURL is the address of Nearhost's page as the home panel shows it, or
// "" while the proxy does not serve it.
func (m *Model) pageURL() string {
	port := m.b.WebPort()
	if port == 0 || !m.started || m.proxyErr != nil {
		return ""
	}
	return fmt.Sprintf("https://%s:%d", hostname.PageHost, port)
}

// webLines link to Nearhost's page, once the proxy serves it. The link is
// the plain address: a click opens it through OpenPage, with the key, and
// the key is never drawn on the screen.
func (m *Model) webLines(width int) []string {
	url := m.pageURL()
	if url == "" {
		return nil
	}
	return field("page", styleBeacon.Underline(true).Hyperlink(url).Render(url), width)
}

// goos is the operating system, a var so tests can pick one.
var goos = runtime.GOOS

// problem is something that needs the user: what it is, the message that
// says why and how to fix it, and what it stops.
type problem struct {
	dot          seg
	title, what  string
	msg          *report.Message
	blocks       []string
	server, host string // the server it is about, which r and y act on
	retry        bool   // r on Home starts the proxy again
}

// problems are what needs the user: the proxy being down, servers that
// cannot connect or whose agents cannot reach Nearhost, and projects in
// error. Each comes with its message, from the state: the code that found
// the problem wrote its fix.
func (m *Model) problems() []problem {
	var out []problem
	if m.proxyErr != nil {
		out = append(out, problem{dot: glyph(markError), title: "proxy", what: "down", msg: proxyProblem(m.proxyErr), retry: true})
	}
	for _, srv := range m.cfg.Servers {
		h := m.b.ServerHealth(srv.Name)
		if h.Status == transport.StatusConnected && h.AgentErr != "" {
			// Not one for r on Home to retry: the server's projects serve,
			// and reconnecting would drop them for nothing.
			out = append(out, problem{dot: glyph(markAttention), title: srv.Name, what: "agents unavailable", msg: agentProblem(h)})
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
		msg := serverProblem(h, srv)
		pr := problem{dot: serverMark(h.Status), title: srv.Name, what: m.connectionText(h), msg: msg,
			blocks: on, server: srv.Name, host: srv.SSHHost}
		// Neither r nor the ssh command that y copies helps a run
		// directory that is too long, so Home offers neither for it.
		if !reconnectHelps(msg) {
			pr.server, pr.host = "", ""
		}
		out = append(out, pr)
	}
	for _, p := range m.cfg.Projects {
		st := m.b.Status(p.Name)
		if st.State != engine.StateError {
			continue
		}
		out = append(out, problem{dot: glyph(markError), title: p.Name, what: "error", msg: projectProblem(st)})
	}
	return out
}

// proxyProblem is the message of the proxy being down for err.
func proxyProblem(err error) *report.Message {
	msg := report.From(err)
	msg.Context = "" // "start proxy" is what the panel's heading says
	return msg
}

// agentProblem is the message of agents on a server that cannot reach
// Nearhost: the one the server's health carries, or one made from its text.
func agentProblem(h engine.ServerHealth) *report.Message {
	if h.AgentReport != nil {
		return h.AgentReport
	}
	return (&transport.AgentError{Problem: h.AgentProblem, Msg: h.AgentErr}).Report()
}

// serverProblem is the message of a server that cannot connect: the one
// its health carries, or one made from its last error.
func serverProblem(h engine.ServerHealth, srv config.Server) *report.Message {
	if h.Problem != nil {
		return h.Problem
	}
	return transport.ConnectionProblem(h.Status, h.LastError, srv.Name, srv.SSHHost)
}

// projectProblem is the message of a project's state: the one its status
// carries, or one made from its text; nil when it has neither.
func projectProblem(st engine.ProjectStatus) *report.Message {
	if st.Problem != nil {
		return st.Problem
	}
	if st.Message == "" {
		return nil
	}
	return report.Errorf(report.Unexpected, "%s", st.Message)
}

// reconnectHelps reports whether reconnecting can fix a server's problem:
// not one of a run directory too long for ssh's control socket, which
// Nearhost reads as it starts.
func reconnectHelps(msg *report.Message) bool {
	return msg == nil || msg.Code != report.SSHSocketPathTooLong
}

// problemFields draws msg as the fields of a panel, width wide: why, which
// is its headline and its explanation, then the fix, each step's text and
// its command on a line of its own that is never wrapped, and the doctor
// line. A step its headline already says is left out. hint, when not "",
// ends the fix with the key that acts on it here, such as "r reconnect".
func problemFields(msg *report.Message, hint footerItem, width int) []string {
	if msg == nil {
		return nil
	}
	why := headline(msg)
	if msg.Why != "" {
		why += " " + prose(msg.Why)
	}
	return append(field("why", why, width), fixFields(msg, hint, width)...)
}

// fixFields draws the fix of msg as problemFields does, without the why.
func fixFields(msg *report.Message, hint footerItem, width int) []string {
	var lines []string
	label := "fix"
	add := func(value string) {
		lines = append(lines, field(label, value, width)...)
		label = ""
	}
	for _, s := range msg.Fix {
		text, c := prose(s.Text), oneLine(s.Command)
		if c == "" && (text == "" || strings.Contains(headline(msg), text)) {
			continue
		}
		if text != "" {
			add(text)
		}
		if c != "" {
			lead := styleLabel.Render(label)
			if label == "" {
				lead = strings.Repeat(" ", labelWidth)
			}
			lines = append(lines, lead+"  "+styleBeacon.Render(c))
			label = ""
		}
	}
	if msg.Doctor {
		add(report.DoctorLine)
	}
	if hint.key != "" {
		add(styleBeacon.Render(hint.key) + styleDim.Render(" "+hint.label))
	}
	return lines
}

// problemLines draw the problems under a heading, with the keys that act on
// servers that cannot connect.
func (m *Model) problemLines(width int) []string {
	problems := m.problems()
	if len(problems) == 0 {
		return nil
	}
	lines := []string{"", section("problems", "", width)}
	servers, retry := false, false
	for _, p := range problems {
		retry = retry || p.retry
		lines = append(lines, "", p.dot.String()+" "+styleTitle.Render(clean(p.title))+"  "+p.dot.style.Render(p.what))
		indent := func(fields []string) {
			for _, l := range fields {
				lines = append(lines, "  "+l)
			}
		}
		indent(problemFields(p.msg, footerItem{}, width-2))
		if len(p.blocks) > 0 {
			indent(field("blocks", strings.Join(p.blocks, ", "), width-2))
		}
		servers = servers || p.server != ""
	}
	// One r retries the proxy and the servers together, so it is named once.
	switch {
	case servers:
		lines = append(lines, "", "  "+styleBeacon.Render("r")+styleDim.Render(" retry now · ")+styleBeacon.Render("y")+styleDim.Render(" copy command"))
	case retry:
		lines = append(lines, "", "  "+styleBeacon.Render("r")+styleDim.Render(" tries again"))
	}
	return lines
}

// retryProxy starts the engine again when the proxy is down, say after the
// program that held a port has quit. It does nothing while a start is
// running or when the proxy is up. The answer is a startedMsg, which starts
// the projects as the first start's does.
func (m *Model) retryProxy() tea.Cmd {
	if m.proxyErr == nil || m.proxyRetrying || m.quitting {
		return nil
	}
	m.proxyRetrying, m.proxyErr = true, nil // Home shows it starting, as the first start does
	b := m.b
	return m.work.do(func(ctx context.Context) tea.Msg {
		return startedMsg{b.Start(ctx)}
	})
}

// retryHome is Home's r: it starts the proxy again when it is down and
// reconnects every server that cannot connect.
func (m *Model) retryHome() tea.Cmd {
	return tea.Batch(m.retryProxy(), m.retryServers())
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
	ca := glyph(markRunning).String() + " trusted"
	if !m.caTrusted {
		ca = glyph(markAttention).String() + styleAmber.Render(" not trusted") + ". Run nearhost trust"
	}
	if m.caExpires.IsZero() {
		return ca
	}
	date := m.caExpires.Format("2006-01-02")
	switch left := m.caExpires.Sub(m.now()); {
	case left <= 0:
		return glyph(markError).String() + styleRed.Render(" expired "+date)
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
