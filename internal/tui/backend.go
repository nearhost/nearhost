// Package tui is the full-screen interface that bare `nearhost` opens: a
// sidebar of servers and projects, and a main panel with the selected item's
// details and log.
package tui

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// Backend is everything the interface needs from the rest of nearhost. The
// CLI wires it to the engine and the machine; tests use a fake. Every method
// may be called from any goroutine.
type Backend interface {
	// Start starts the engine: it checks the proxy ports and creates the
	// local CA. An error leaves the proxy down; projects cannot start.
	Start(ctx context.Context) error
	// Shutdown stops every project and the engine. ctx ending cuts it short.
	Shutdown(ctx context.Context) error
	// Config is the configuration the engine runs.
	Config() *config.Config
	// ConfigPath is the config file, which may not exist yet.
	ConfigPath() string
	// Apply moves the engine to cfg.
	Apply(ctx context.Context, cfg *config.Config) error
	Status(project string) engine.ProjectStatus
	ServerStatus(server string) (transport.Status, string)
	Events() *engine.Bus
	StartProject(ctx context.Context, name string) error
	StopProject(ctx context.Context, name string) error
	// Ports are the proxy's HTTPS and HTTP ports.
	Ports() (https, http int)
	// CATrusted reports whether the system trusts the local CA.
	CATrusted() bool
	// HostsState describes the nearhost block in /etc/hosts in a few words.
	HostsState() string
	// Open opens url in the browser.
	Open(url string) error
	// AddCommand is the command that asks the add questions for the project
	// in dir, in the plain terminal while the interface is suspended.
	AddCommand(dir string) tea.ExecCommand
}

// Suspender runs functions that need the terminal, such as sudo asking for a
// password, while the interface is suspended. Attach it to the running
// program first; until then Run fails.
type Suspender struct {
	p atomic.Pointer[tea.Program]
}

// Attach makes s suspend p.
func (s *Suspender) Attach(p *tea.Program) { s.p.Store(p) }

// errNotRunning is returned by Run before the interface runs.
var errNotRunning = errors.New("the interface is not running")

// Run suspends the interface, runs fn with the terminal's input and output,
// and resumes the interface. It returns fn's error, or ctx's when ctx ends
// first; fn then still runs and is expected to notice ctx itself.
func (s *Suspender) Run(ctx context.Context, fn func(in io.Reader, out io.Writer) error) error {
	p := s.p.Load()
	if p == nil {
		return errNotRunning
	}
	done := make(chan error, 1)
	p.Send(suspendMsg{fn: fn, done: done})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// suspendMsg asks the model to run fn with the terminal.
type suspendMsg struct {
	fn   func(in io.Reader, out io.Writer) error
	done chan<- error
}

// Command makes fn a command the interface can hand the terminal to, as
// AddCommand returns.
func Command(fn func(in io.Reader, out io.Writer) error) tea.ExecCommand {
	return &funcCommand{fn: fn}
}

// funcCommand runs a function as a tea.ExecCommand.
type funcCommand struct {
	fn       func(in io.Reader, out io.Writer) error
	in       io.Reader
	out, err io.Writer
}

func (c *funcCommand) Run() error            { return c.fn(c.in, c.out) }
func (c *funcCommand) SetStdin(r io.Reader)  { c.in = r }
func (c *funcCommand) SetStdout(w io.Writer) { c.out = w }
func (c *funcCommand) SetStderr(w io.Writer) { c.err = w }
