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

	// SSHHosts lists the hosts named in the user's OpenSSH config.
	SSHHosts() []string
	// CheckServer connects to srv as adding a project there would, and
	// disconnects. Its error says how to fix access.
	CheckServer(ctx context.Context, srv config.Server) error
	// AddServer saves srv.
	AddServer(srv config.Server) error
	// OpenServer opens srv's files, for adding a project there.
	OpenServer(ctx context.Context, srv config.Server) (ServerFiles, error)
	// RegistryQuestion reports whether to ask about trusting the official
	// preset registry: none is trusted and the question is not turned off.
	RegistryQuestion() bool
	// AnswerRegistry does what the answer says. It returns a note when the
	// registry could not be trusted, and an error only when the answer could
	// not be saved.
	AnswerRegistry(ctx context.Context, answer RegistryAnswer) (string, error)
	// EnablePresets enables presets a registry suggested, returning a note
	// for each that could not be.
	EnablePresets(ctx context.Context, names []string) []string
	// CheckNames says what is wrong with a new project's name and alias, nil
	// for each that is fine.
	CheckNames(name, alias string) (nameErr, aliasErr error)
	// SaveProject saves a new project.
	SaveProject(p config.Project) error
}

// ServerFiles is the files of a server, open while a project there is being
// added. Close ends it.
type ServerFiles interface {
	// ListDirs lists the directories in dir, which may start with ~.
	ListDirs(ctx context.Context, dir string) ([]string, error)
	// Resolve works out the directory a path means and checks it is one.
	Resolve(ctx context.Context, path string) (string, error)
	// Discover reads the project in the directory path means.
	Discover(ctx context.Context, path string) (Draft, error)
	Close() error
}

// Draft is what discovery found for a project being added.
type Draft struct {
	Server      config.Server
	Path        string // resolved
	Name, Alias string // defaults
	Routes      []config.Route
	Presets     []SuggestedPreset
	Advisories  []Advisory
	Notes       []string
}

// SuggestedPreset is a preset a trusted registry offers for the project.
type SuggestedPreset struct {
	Name  string // <registry>/<name>
	Title string
	Repo  string // the registry's repository
}

// Advisory is advice about the project's setup.
type Advisory struct {
	Code, Message, Snippet string
}

// RegistryAnswer answers whether to trust the official preset registry.
type RegistryAnswer int

const (
	RegistryYes RegistryAnswer = iota
	RegistryNo
	RegistryNever // don't ask again
)

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

// errQuitting answers a request for the terminal made while quitting.
var errQuitting = errors.New("nearhost is quitting")

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
