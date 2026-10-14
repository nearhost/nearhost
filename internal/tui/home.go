package tui

import (
	"fmt"

	"github.com/nearhost/nearhost/internal/engine"
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
			lines = append(m.proxyLines(width), "")
		}
		lines = append(lines, markLines...)
		return append(lines, "",
			styleTitle.Render("Nothing to serve yet."),
			"Press n to add the project in the current directory.")
	}
	ca := styleBeacon.Render("trusted")
	if !m.caTrusted {
		ca = styleAmber.Render("not trusted") + ". Run nearhost trust"
	}
	running := 0
	for _, p := range m.cfg.Projects {
		if m.b.Status(p.Name).State == engine.StateRunning {
			running++
		}
	}
	lines := []string{styleTitle.Render("Nearhost"), ""}
	lines = append(lines, m.proxyLines(width)...)
	lines = append(lines, field("local CA", ca, width)...)
	lines = append(lines, field("hosts", clean(m.hostsState), width)...)
	lines = append(lines, field("config", clean(m.b.ConfigPath()), width)...)
	return append(lines, field("running", fmt.Sprintf("%d of %d", running, len(m.cfg.Projects)), width)...)
}

// proxyLines say whether the proxy is up, and how to get round a port that
// is taken when it is not.
func (m *Model) proxyLines(width int) []string {
	https, http := m.b.Ports()
	switch {
	case m.proxyErr != nil:
		lines := field("proxy", styleRed.Render("● down: ")+clean(m.proxyErr.Error()), width)
		return append(lines, field("fix", "Quit the program using the port, or run nearhost with --https-port and --http-port.", width)...)
	case !m.started:
		return field("proxy", styleAmber.Render("◐ starting"), width)
	}
	return field("proxy", styleBeacon.Render("●")+fmt.Sprintf(" https :%d · http :%d", https, http), width)
}
