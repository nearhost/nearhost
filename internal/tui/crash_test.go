package tui

import (
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/report"
)

// viewBomb panics in View once it is told to, and asks for nothing that
// would bring another message: no clock.
type viewBomb struct{ armed *bool }

type armMsg struct{}

func (v viewBomb) Init() tea.Cmd { return nil }
func (v viewBomb) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(armMsg); ok {
		*v.armed = true
	}
	return v, nil
}
func (v viewBomb) View() tea.View {
	if *v.armed {
		panic("boom in View")
	}
	return tea.NewView("fine")
}

// A panic in View ends the program at once, though no message follows it.
func TestPanicInViewEndsTheProgram(t *testing.T) {
	was := report.CrashDir
	report.CrashDir = t.TempDir()
	t.Cleanup(func() { report.CrashDir = was })
	var c Crashes
	sent := make(chan struct{}, 1)
	p := tea.NewProgram(c.Guard(viewBomb{armed: new(bool)}), tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 24), tea.WithoutSignalHandler(), tea.WithoutCatchPanics(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(crashedMsg); ok {
				select {
				case sent <- struct{}{}:
				default:
				}
			}
			return msg
		}))
	c.Attach(p)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	p.Send(armMsg{})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatal("the program did not end after a panic in View")
	}
	if m := c.Crash(); m == nil || m.Code != report.Crash {
		t.Errorf("crash = %+v", m)
	}
	select {
	case <-sent:
	default:
		t.Error("the panic in View sent nothing to end the program: it waited for another message")
	}
}
