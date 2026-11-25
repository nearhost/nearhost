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

// A panic in View ends the program: the crash is kept, and the program
// ends without the test sending it anything more.
func TestPanicInViewEndsTheProgram(t *testing.T) {
	was := report.CrashDir
	report.CrashDir = t.TempDir()
	t.Cleanup(func() { report.CrashDir = was })
	var c Crashes
	p := tea.NewProgram(c.Guard(viewBomb{armed: new(bool)}), tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 24), tea.WithoutSignalHandler(), tea.WithoutCatchPanics())
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
}

// A panic in View sends the attached program the message that ends it:
// View returns no command, and the program may get no other message.
func TestPanicInViewSendsTheEnd(t *testing.T) {
	was := report.CrashDir
	report.CrashDir = t.TempDir()
	t.Cleanup(func() { report.CrashDir = was })
	var c Crashes
	got := make(chan tea.Msg, 1)
	c.attach(func(msg tea.Msg) { got <- msg })
	armed := true
	c.Guard(viewBomb{armed: &armed}).View()
	select {
	case msg := <-got:
		cm, ok := msg.(crashedMsg)
		if !ok || cm.msg == nil || cm.msg != c.Crash() {
			t.Errorf("sent %#v, want the crash", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the panic in View sent nothing to end the program")
	}
}
