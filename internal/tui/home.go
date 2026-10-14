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

// home is the Home panel.
func (m *Model) home(width int) []string {
	if len(m.cfg.Projects) == 0 {
		lines := append([]string{""}, markLines...)
		return append(lines, "",
			styleTitle.Render("Nothing to serve yet."),
			"Press n to add the project in the current directory.")
	}
	https, http := m.b.Ports()
	proxyLine := styleBeacon.Render("●") + fmt.Sprintf(" https :%d · http :%d", https, http)
	switch {
	case m.proxyErr != nil:
		proxyLine = styleRed.Render("● down: ") + clean(m.proxyErr.Error())
	case !m.started:
		proxyLine = styleAmber.Render("◐ starting")
	}
	ca := styleBeacon.Render("trusted")
	if !m.b.CATrusted() {
		ca = styleAmber.Render("not trusted") + ". Run nearhost trust"
	}
	running := 0
	for _, p := range m.cfg.Projects {
		if m.b.Status(p.Name).State == engine.StateRunning {
			running++
		}
	}
	lines := []string{styleTitle.Render("Nearhost"), ""}
	lines = append(lines, field("proxy", proxyLine, width)...)
	lines = append(lines, field("local CA", ca, width)...)
	lines = append(lines, field("hosts", clean(m.b.HostsState()), width)...)
	lines = append(lines, field("config", clean(m.b.ConfigPath()), width)...)
	return append(lines, field("running", fmt.Sprintf("%d of %d", running, len(m.cfg.Projects)), width)...)
}
