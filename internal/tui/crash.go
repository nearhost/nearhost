package tui

import (
	"runtime/debug"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/report"
)

// Crashes catches the panics of the interface: in its Update, View and
// commands, through Guard, and in the goroutines its backend starts with
// report.Go, as the sink of their context. The first one is kept, its
// stack in a crash file, and ends the program, which restores the
// terminal; the caller then reports it (see Crash). The program must run
// with tea.WithoutCatchPanics, so that bubbletea prints no stack of its own.
type Crashes struct {
	mu    sync.Mutex
	first *report.Message
	p     atomic.Pointer[tea.Program]
}

// crashedMsg ends the program after a crash.
type crashedMsg struct{ msg *report.Message }

// Report keeps m when it is the first crash, and ends the program. It is
// a report.Sink, for report.Go; a message that is not a crash is ignored.
func (c *Crashes) Report(m *report.Message) {
	if m == nil || m.Code != report.Crash || !c.keep(m) {
		return
	}
	if p := c.p.Load(); p != nil {
		go p.Send(crashedMsg{m}) // Send returns at once once the program has ended
	}
}

// keep records m as the crash, and reports whether it was the first.
func (c *Crashes) keep(m *report.Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.first != nil {
		return false
	}
	c.first = m
	return true
}

// Attach makes a crash end p, one that came before it included.
func (c *Crashes) Attach(p *tea.Program) {
	c.p.Store(p)
	if c.Crash() != nil {
		go p.Send(crashedMsg{c.Crash()})
	}
}

// Crash is the first crash, or nil when there was none.
func (c *Crashes) Crash() *report.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first
}

// caught records a panic with value v as a crash, with the stack of the
// goroutine that panicked.
func (c *Crashes) caught(v any) {
	c.keep(report.Crashed(v, debug.Stack()))
}

// Guard returns m with its panics caught: one in Init, Update or View, or
// in a command it returns, is kept as a crash and ends the program. Once
// there has been a crash, m is no longer asked anything.
func (c *Crashes) Guard(m tea.Model) tea.Model { return guarded{m, c} }

type guarded struct {
	m tea.Model
	c *Crashes
}

func (g guarded) Init() (cmd tea.Cmd) {
	defer func() {
		if v := recover(); v != nil {
			g.c.caught(v)
			cmd = tea.Quit
		}
	}()
	return g.c.guard(g.m.Init())
}

func (g guarded) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	if cm, ok := msg.(crashedMsg); ok {
		if cm.msg != nil {
			g.c.keep(cm.msg)
		}
		return g, tea.Quit
	}
	if g.c.Crash() != nil {
		return g, tea.Quit
	}
	defer func() {
		if v := recover(); v != nil {
			g.c.caught(v)
			model, cmd = g, tea.Quit
		}
	}()
	m, cmd := g.m.Update(msg)
	return guarded{m, g.c}, g.c.guard(cmd)
}

// View draws m, or nothing once there has been a crash. A panic while
// drawing is a crash too; the next message, the clock's at the latest,
// ends the program.
func (g guarded) View() (v tea.View) {
	if g.c.Crash() != nil {
		return tea.NewView("")
	}
	defer func() {
		if r := recover(); r != nil {
			g.c.caught(r)
			v = tea.NewView("")
		}
	}()
	return g.m.View()
}

// guard returns cmd with its panics caught, as a crashedMsg; a batch has
// each of its commands guarded.
func (c *Crashes) guard(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() (msg tea.Msg) {
		defer func() {
			if v := recover(); v != nil {
				c.caught(v)
				msg = crashedMsg{c.Crash()}
			}
		}()
		msg = cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			guarded := make(tea.BatchMsg, len(batch))
			for i, b := range batch {
				guarded[i] = c.guard(b)
			}
			return guarded
		}
		return msg
	}
}
