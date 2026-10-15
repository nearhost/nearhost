package tui

import (
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// newSpinner is the spinner of projects that are starting and servers that
// are connecting. Its frames are plain; whoever draws one colours it.
func newSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.MiniDot))
}

// busy reports whether anything shown turns the spinner: a project that is
// starting, or a server that is connecting.
func (m *Model) busy() bool {
	for _, p := range m.cfg.Projects {
		if m.b.Status(p.Name).State == engine.StateStarting {
			return true
		}
	}
	for _, s := range m.cfg.Servers {
		if s.Name == config.LocalServer {
			continue
		}
		switch st, _ := m.b.ServerStatus(s.Name); st {
		case transport.StatusConnecting, transport.StatusReconnecting:
			return true
		}
	}
	return false
}

// spin starts the spinner when something is busy and it is not turning.
func (m *Model) spin() tea.Cmd {
	if m.spinning || m.quitting || !m.busy() {
		return nil
	}
	m.spinning = true
	return m.spinner.Tick
}

// turn moves the spinner on a frame, or stops it when nothing is busy.
func (m *Model) turn(msg spinner.TickMsg) tea.Cmd {
	if !m.busy() || m.quitting {
		m.spinning = false
		return nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return cmd
}
