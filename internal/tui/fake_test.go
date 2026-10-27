package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/transport"
)

// fakeBackend records what the interface asks of it.
type fakeBackend struct {
	mu        sync.Mutex
	webPort   int
	cfg       *config.Config
	path      string
	bus       *engine.Bus
	states    map[string]engine.ProjectStatus
	servers   map[string]transport.Status
	srvErrs   map[string]string
	health    map[string]engine.ServerHealth // failures and next attempts, by server
	startErr  error
	startErr2 error // what StartProject returns
	reads     int   // calls to CATrusted and HostsState
	openErr   error
	applyErr  error
	forms     formFake
	edit      editFake
	change    changeFake
	trusted   bool
	caExpires time.Time
	// hostsState is what HostsState says; empty means "2 names".
	hostsState string
	// applyBlocks makes Apply wait for its context to end.
	applyBlocks bool

	started  []string
	stopped  []string
	applied  []*config.Config
	opened   []string
	shutdown []context.Context
	answers  []string // "accept <id> <name>" or "decline <id> <reason>"

	// passes are what Passthroughs lists, and grantList what Grants lists.
	passes    []engine.Passthrough
	grantList []grants.Grant
	// passCalls records the passthrough calls, and passErr is what they return.
	passCalls []string
	passErr   error
	// passEnded holds the requests that have ended, with the error a Yes or
	// Always allow on them returns, as the agent API does once an answer
	// fails or the request expires or its session ends. A No on them
	// returns it too.
	passEnded map[string]error
	// passErrKeeps makes a failed Yes or Always allow leave its request
	// waiting, as an error that is not the request's leaves it.
	passErrKeeps bool
}

func newFake(t *testing.T, cfg *config.Config) *fakeBackend {
	t.Helper()
	return &fakeBackend{
		cfg: cfg, path: filepath.Join(t.TempDir(), "config.yaml"), bus: engine.NewBus(),
		states: map[string]engine.ProjectStatus{}, servers: map[string]transport.Status{},
		srvErrs: map[string]string{}, health: map[string]engine.ServerHealth{}, trusted: true, webPort: 6327,
	}
}

func (f *fakeBackend) Start(context.Context) error { return f.startErr }
func (f *fakeBackend) AcceptProposal(id, name string) {
	f.mu.Lock()
	f.answers = append(f.answers, "accept "+id+" "+name)
	f.mu.Unlock()
}
func (f *fakeBackend) DeclineProposal(id, reason string) {
	f.mu.Lock()
	f.answers = append(f.answers, strings.TrimSpace("decline "+id+" "+reason))
	f.mu.Unlock()
}

// Passthroughs records nothing and lists the fake's passthroughs.
func (f *fakeBackend) Passthroughs(server string) []engine.Passthrough {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []engine.Passthrough
	for _, p := range f.passes {
		if server == "" || p.Server == server {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeBackend) ClosePassthrough(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passCalls = append(f.passCalls, "close "+id)
	return f.passErr
}

func (f *fakeBackend) AnswerPassthrough(_ context.Context, id string, a Answer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passCalls = append(f.passCalls, fmt.Sprintf("answer %s %d", id, a))
	if err, ok := f.passEnded[id]; ok {
		return err
	}
	if errors.Is(f.passErr, ErrAllowNotSaved) {
		return f.passErr // the port opened, and the request was answered
	}
	if f.passErr != nil && a != AnswerNo && !f.passErrKeeps {
		// A failed open ends the request.
		f.endPass(id, f.passErr)
		return f.passEnded[id]
	}
	return f.passErr
}

// endPass ends the request id, so a Yes or Always allow on it returns err,
// marked as ending it. The caller holds f.mu.
func (f *fakeBackend) endPass(id string, err error) {
	if f.passEnded == nil {
		f.passEnded = map[string]error{}
	}
	f.passEnded[id] = RequestEnded(err)
}

func (f *fakeBackend) Grants(server string) ([]grants.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []grants.Grant
	for _, g := range f.grantList {
		if g.Server == server { // an empty server matches none, as in Backend
			out = append(out, g)
		}
	}
	return out, f.passErr
}

func (f *fakeBackend) RevokeGrant(server, requester string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passCalls = append(f.passCalls, "revoke "+server+" "+requester)
	return f.passErr
}

// passthroughCalls are the passthrough calls made, in order.
func (f *fakeBackend) passthroughCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.passCalls...)
}

func (f *fakeBackend) proposalAnswers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.answers...)
}
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
func (f *fakeBackend) ServerHealth(name string) engine.ServerHealth {
	st, msg := f.ServerStatus(name)
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.health[name]
	h.Status, h.LastError = st, msg
	return h
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

func (f *fakeBackend) WebPort() int { return f.webPort }
func (f *fakeBackend) CAExpires() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caExpires
}
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
	if f.hostsState != "" {
		return f.hostsState
	}
	return "2 names"
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
// Tests never touch the clipboard of the machine they run on.
// Nor do they wait for toasts to go.
func init() {
	systemClipboard = func(string) error { return nil }
	toastTimer = func(int) tea.Cmd { return nil }
}

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
	case "right":
		k = tea.KeyPressMsg{Code: tea.KeyRight}
	case "backspace":
		k = tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		k = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		k = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "ctrl+r":
		k = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
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

// click presses and releases the left button at x, y, and returns what the
// release does.
func click(m *Model, x, y int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return cmd
}

// run runs cmd and feeds its message back to the model, once. A batch has
// each of its commands run so, and what they lead to is batched.
func run(m *Model, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var next []tea.Cmd
		for _, c := range batch {
			next = append(next, run(m, c))
		}
		return tea.Batch(next...)
	}
	_, next := m.Update(msg)
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

// wantRow fails unless the screen has a line holding every one of subs, in
// order: one rendered row, with what it should show.
func wantRow(t *testing.T, m *Model, subs ...string) {
	t.Helper()
	s := screen(m)
	for _, l := range strings.Split(s, "\n") {
		rest, ok := l, true
		for _, sub := range subs {
			i := strings.Index(rest, sub)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(sub):]
		}
		if ok {
			return
		}
	}
	t.Errorf("no row holds %q:\n%s", subs, s)
}

// wantText fails unless the screen holds every one of subs as text (see
// screenText), wherever the lines wrap it.
func wantText(t *testing.T, m *Model, subs ...string) {
	t.Helper()
	text := screenText(m)
	for _, sub := range subs {
		if !strings.Contains(text, sub) {
			t.Errorf("screen lacks %q:\n%s", sub, screen(m))
		}
	}
}

// screenText is the screen read as text across the lines a form's box wraps
// it onto: each line's content between the box's edges, joined with single
// spaces.
func screenText(m *Model) string {
	var parts []string
	for _, l := range strings.Split(screen(m), "\n") {
		if i, j := strings.Index(l, "│"), strings.LastIndex(l, "│"); i >= 0 && j > i {
			l = l[i+len("│") : j]
		}
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

var errBoom = errors.New("boom")

// logText is the log of the row with key, an entry a line.
func logText(m *Model, key string) string {
	var lines []string
	for _, e := range m.logs.entries(key, "") {
		lines = append(lines, e.plain())
	}
	return strings.Join(lines, "\n")
}
