package tui

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// fakeBackend records what the interface asks of it.
type fakeBackend struct {
	mu        sync.Mutex
	cfg       *config.Config
	path      string
	bus       *engine.Bus
	states    map[string]engine.ProjectStatus
	servers   map[string]transport.Status
	srvErrs   map[string]string
	startErr  error
	startErr2 error // what StartProject returns
	reads     int   // calls to CATrusted and HostsState
	openErr   error
	applyErr  error
	forms     formFake
	trusted   bool
	// applyBlocks makes Apply wait for its context to end.
	applyBlocks bool

	started  []string
	stopped  []string
	applied  []*config.Config
	opened   []string
	addDirs  []string
	shutdown []context.Context
}

func newFake(t *testing.T, cfg *config.Config) *fakeBackend {
	t.Helper()
	return &fakeBackend{
		cfg: cfg, path: filepath.Join(t.TempDir(), "config.yaml"), bus: engine.NewBus(),
		states: map[string]engine.ProjectStatus{}, servers: map[string]transport.Status{},
		srvErrs: map[string]string{}, trusted: true,
	}
}

func (f *fakeBackend) Start(context.Context) error { return f.startErr }
func (f *fakeBackend) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	f.shutdown = append(f.shutdown, ctx)
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) Config() *config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}
func (f *fakeBackend) ConfigPath() string { return f.path }
func (f *fakeBackend) Apply(ctx context.Context, cfg *config.Config) error {
	f.mu.Lock()
	blocks := f.applyBlocks
	f.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, cfg)
	if f.applyErr != nil {
		return f.applyErr
	}
	f.cfg = cfg
	return nil
}
func (f *fakeBackend) Status(name string) engine.ProjectStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.states[name]; ok {
		return st
	}
	return engine.ProjectStatus{State: engine.StateStopped}
}
func (f *fakeBackend) ServerStatus(name string) (transport.Status, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == config.LocalServer {
		return transport.StatusConnected, ""
	}
	st, ok := f.servers[name]
	if !ok {
		st = transport.StatusDisconnected
	}
	return st, f.srvErrs[name]
}
func (f *fakeBackend) Events() *engine.Bus { return f.bus }
func (f *fakeBackend) StartProject(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, name)
	return f.startErr2
}
func (f *fakeBackend) StopProject(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	return nil
}
func (f *fakeBackend) Ports() (int, int) { return 443, 80 }
func (f *fakeBackend) CATrusted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.trusted
}
func (f *fakeBackend) HostsState() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return "2 aliases"
}
func (f *fakeBackend) machineReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}
func (f *fakeBackend) Open(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, url)
	return f.openErr
}
func (f *fakeBackend) AddCommand(dir string) tea.ExecCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addDirs = append(f.addDirs, dir)
	return &funcCommand{fn: func(io.Reader, io.Writer) error { return nil }}
}

func (f *fakeBackend) setState(name string, st engine.ProjectStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[name] = st
}

// sampleConfig has a local project and two on a server.
func sampleConfig() *config.Config {
	return &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini.lan"}},
		Projects: []config.Project{
			{Name: "website", Server: config.LocalServer, Path: "/code/website", Alias: "website.test", Enabled: true,
				Routes: []config.Route{{Port: 8080, Role: config.RoleWeb, Label: "app (APP_PORT)"}}},
			{Name: "myapp", Server: "mini", Path: "/srv/myapp", Alias: "myapp.test", Enabled: true,
				Routes: []config.Route{
					{Port: 80, Role: config.RoleWeb, Label: "web"},
					{Port: 5173, Role: config.RoleLoopback, Label: "vite (VITE_PORT)"},
					{Port: 3306, Role: config.RoleIgnore, Label: "db"},
				}},
			{Name: "old", Server: "mini", Path: "/srv/old", Alias: "old.test", Enabled: false,
				Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}},
		},
	}
}

// sized is a model for b in a 100×30 window.
func sized(b Backend) *Model {
	m := New(b, "/work/here")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// screen is what the model draws, without colours.
func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

func press(m *Model, key string) tea.Cmd {
	var k tea.KeyPressMsg
	switch key {
	case "enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		k = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		k = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		k = tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		k = tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		k = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		k = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "tab":
		k = tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		k = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		r := []rune(key)[0]
		k = tea.KeyPressMsg{Code: r, Text: key}
	}
	_, cmd := m.Update(k)
	return cmd
}

// run runs cmd and feeds its message back to the model, once.
func run(m *Model, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	_, next := m.Update(cmd())
	return next
}

func wantScreen(t *testing.T, m *Model, subs ...string) {
	t.Helper()
	s := screen(m)
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("screen lacks %q:\n%s", sub, s)
		}
	}
}

var errBoom = errors.New("boom")

func (f *fakeBackend) appliedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}
