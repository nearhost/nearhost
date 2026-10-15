package tui

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
)

// formFake is the part of the fake backend the add forms use. Its fields
// are set by tests before the form runs.
type formFake struct {
	hosts       []string
	checkErr    error
	checkBlocks bool // CheckServer waits for its context to end
	checked     []config.Server
	addedSrv    []config.Server
	openErr     error
	opened      []config.Server
	files       *fakeFiles
	askRegistry bool
	answers     []RegistryAnswer
	answerNote  string
	enabled     [][]string
	enableNotes []string
	nameErr     error
	aliasErr    error
	saveErr     error
	saved       []config.Project
}

// fakeFiles is a server's files: dirs lists every directory, by path, and
// drafts what discovery finds in each. drafts are looked up again after
// presets are enabled, under the key "<path>+presets".
type fakeFiles struct {
	home    string
	dirs    map[string]bool
	drafts  map[string]Draft
	listErr error
	closed  int
	presets bool     // EnablePresets has run
	advised []string // the aliases Advise was asked about
}

func (f *fakeFiles) abs(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = f.home + p[1:]
	}
	return path.Clean(p)
}

func (f *fakeFiles) ListDirs(_ context.Context, dir string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	dir = f.abs(dir)
	if !f.dirs[dir] {
		return nil, errors.New(dir + ": no such directory")
	}
	var names []string
	for d := range f.dirs {
		if path.Dir(d) == dir && d != dir {
			names = append(names, path.Base(d))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeFiles) Resolve(_ context.Context, p string) (string, error) {
	p = f.abs(p)
	if !f.dirs[p] {
		return "", errors.New(p + ": no such directory")
	}
	return p, nil
}

func (f *fakeFiles) Discover(ctx context.Context, p string) (Draft, error) {
	p, err := f.Resolve(ctx, p)
	if err != nil {
		return Draft{}, err
	}
	key := p
	if f.presets {
		key += "+presets"
	}
	d, ok := f.drafts[key]
	if !ok {
		d = f.drafts[p]
	}
	d.Path = p
	if d.Name == "" {
		d.Name = path.Base(p)
		d.Alias = d.Name + ".test"
	}
	return d, nil
}

func (f *fakeFiles) Close() error { f.closed++; return nil }

func (f *fakeBackend) SSHHosts() []string { return f.forms.hosts }

func (f *fakeBackend) CheckServer(ctx context.Context, srv config.Server) error {
	f.mu.Lock()
	f.forms.checked = append(f.forms.checked, srv)
	blocks, err := f.forms.checkBlocks, f.forms.checkErr
	f.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeBackend) AddServer(srv config.Server) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forms.addedSrv = append(f.forms.addedSrv, srv)
	return nil
}

func (f *fakeBackend) OpenServer(_ context.Context, srv config.Server) (ServerFiles, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forms.opened = append(f.forms.opened, srv)
	if f.forms.openErr != nil {
		return nil, f.forms.openErr
	}
	return f.forms.files, nil
}

func (f *fakeBackend) RegistryQuestion() bool { return f.forms.askRegistry }

func (f *fakeBackend) AnswerRegistry(_ context.Context, a RegistryAnswer) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forms.answers = append(f.forms.answers, a)
	return f.forms.answerNote, nil
}

func (f *fakeBackend) EnablePresets(_ context.Context, names []string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forms.enabled = append(f.forms.enabled, names)
	if f.forms.files != nil {
		f.forms.files.presets = true
	}
	return f.forms.enableNotes
}

func (f *fakeBackend) CheckNames(string, string) (error, error) {
	return f.forms.nameErr, f.forms.aliasErr
}

func (f *fakeBackend) SaveProject(p config.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forms.saveErr != nil {
		return f.forms.saveErr
	}
	f.forms.saved = append(f.forms.saved, p)
	return nil
}

func (f *fakeFiles) Advise(_ context.Context, _ string, routes []config.Route, alias string) ([]Advisory, []string, error) {
	f.advised = append(f.advised, alias)
	return []Advisory{{Code: "app-url", Message: "APP_URL should be https://" + alias + "."}}, nil, nil
}

// editFake is the part of the fake backend the editing tests use.
type editFake struct {
	editErr      error
	edits        int
	reconnected  []string
	dropped      []string
	captured     map[string]proxy.Exchange
	replayed     []string
	connected    []string
	reconnectErr error
	advice       map[string][]Advisory
	presets      map[string][]string
	adviceErr    error
	adviceAsked  []string
	trustErr     error
	trusts       int
}

func (f *fakeBackend) EditConfig(fn func(cfg *config.Config) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.edit.editErr != nil {
		return f.edit.editErr
	}
	cfg := cloneForTest(f.cfg)
	if err := fn(cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	f.cfg = cfg
	f.edit.edits++
	return nil
}

func (f *fakeBackend) ReconnectServer(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edit.reconnected = append(f.edit.reconnected, name)
	return f.edit.reconnectErr
}

func (f *fakeBackend) Captured(id string) (proxy.Exchange, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ex, ok := f.edit.captured[id]
	return ex, ok
}

func (f *fakeBackend) Replay(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edit.replayed = append(f.edit.replayed, id)
	return 500, nil
}

func (f *fakeBackend) DisconnectServer(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edit.dropped = append(f.edit.dropped, name)
	return nil
}

func (f *fakeBackend) ConnectServer(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edit.connected = append(f.edit.connected, name)
	return nil
}

func (f *fakeBackend) Advice(_ context.Context, project string) (ProjectAdvice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edit.adviceAsked = append(f.edit.adviceAsked, project)
	return ProjectAdvice{Items: f.edit.advice[project], Presets: f.edit.presets[project]}, f.edit.adviceErr
}

func (f *fakeBackend) Trust(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edit.trusts++
	if f.edit.trustErr == nil {
		f.trusted = true
	}
	return f.edit.trustErr
}

// cloneForTest copies a config deeply enough for an edit to leave the
// original as it was.
func cloneForTest(cfg *config.Config) *config.Config {
	out := *cfg
	out.Servers = append([]config.Server(nil), cfg.Servers...)
	out.Projects = append([]config.Project(nil), cfg.Projects...)
	for i := range out.Projects {
		out.Projects[i].Routes = append([]config.Route(nil), out.Projects[i].Routes...)
	}
	return &out
}
