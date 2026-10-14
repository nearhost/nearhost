package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/tui"
)

// What the interface's add forms ask of nearhost. Each call reads the config
// file afresh, so a form never saves over a change made while it was open.

// current reads and checks the config file.
func (u *uiEngine) current() (*config.Config, error) {
	cfg, err := config.Load(u.path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", u.path, err)
	}
	return cfg, nil
}

// checkThen is what to do after fixing access, in the interface's words.
const checkThen = "press r to try again"

func (u *uiEngine) SSHHosts() []string {
	if u.b.sshConfig == nil {
		return nil
	}
	data, err := u.b.sshConfig()
	if err != nil {
		return nil
	}
	return sshHosts(data)
}

func (u *uiEngine) CheckServer(ctx context.Context, srv config.Server) error {
	files, err := u.b.up.ssh.openServer(ctx, srv, checkThen)
	if err != nil {
		return err
	}
	return files.close()
}

func (u *uiEngine) AddServer(srv config.Server) error {
	cfg, err := u.current()
	if err != nil {
		return err
	}
	return addServer(cfg, u.path, srv)
}

func (u *uiEngine) OpenServer(ctx context.Context, srv config.Server) (tui.ServerFiles, error) {
	files, err := openFiles(ctx, u.b.up.ssh, srv, checkThen)
	if err != nil {
		return nil, err
	}
	return &uiFiles{u: u, srv: srv, files: files}, nil
}

func (u *uiEngine) RegistryQuestion() bool {
	cfg, err := u.current()
	return err == nil && len(cfg.Registries) == 0 && !cfg.RegistryPromptOff()
}

func (u *uiEngine) AnswerRegistry(ctx context.Context, answer tui.RegistryAnswer) (string, error) {
	cfg, err := u.current()
	if err != nil {
		return "", err
	}
	a := registryNo
	switch answer {
	case tui.RegistryYes:
		a = registryYes
	case tui.RegistryNever:
		a = registryNever
	}
	return answerRegistry(ctx, io.Discard, u.b.presets, cfg, u.path, a)
}

func (u *uiEngine) EnablePresets(ctx context.Context, names []string) []string {
	cfg, err := u.current()
	if err != nil {
		return []string{err.Error()}
	}
	var notes []string
	for _, name := range names {
		if note := enableOffered(ctx, u.b.presets, cfg, u.path, name); note != "" {
			notes = append(notes, note)
		}
	}
	return notes
}

func (u *uiEngine) CheckNames(name, alias string) (error, error) {
	cfg, err := u.current()
	if err != nil {
		return err, nil
	}
	return formNameProblem(cfg, name), aliasProblem(cfg, alias)
}

func (u *uiEngine) SaveProject(p config.Project) error {
	cfg, err := u.current()
	if err != nil {
		return err
	}
	if err := errors.Join(formNameProblem(cfg, p.Name), aliasProblem(cfg, p.Alias)); err != nil {
		return err
	}
	return saveProject(cfg, u.path, p)
}

// formNameProblem is nameProblem in the words of the add form.
func formNameProblem(cfg *config.Config, name string) error {
	var taken *nameTakenError
	switch err := nameProblem(cfg, name); {
	case errors.As(err, &taken):
		return fmt.Errorf("a project called %q already exists, on %s", name, taken.other.Server)
	default:
		return err
	}
}

// uiFiles is a server's files for the add form.
type uiFiles struct {
	u     *uiEngine
	srv   config.Server
	files serverFiles
}

func (f *uiFiles) ListDirs(_ context.Context, dir string) ([]string, error) {
	resolved, err := f.files.resolve(dir)
	if err != nil {
		return nil, err
	}
	return f.files.listDirs(resolved)
}

func (f *uiFiles) Resolve(_ context.Context, path string) (string, error) {
	return f.files.resolve(path)
}

// Discover runs discovery with the enabled presets and returns what the add
// form shows: the routes, the presets the trusted registries suggest,
// advisories and notes.
func (f *uiFiles) Discover(ctx context.Context, path string) (tui.Draft, error) {
	cfg, err := f.u.current()
	if err != nil {
		return tui.Draft{}, err
	}
	dir, err := f.files.resolve(path)
	if err != nil {
		return tui.Draft{}, err
	}
	sources := f.u.b.presets.indexes(ctx, cfg, f.u.path)
	reader := f.files.reader(dir)
	res, warnings, err := discoverOnce(cfg, f.u.path, reader, sources, nil)
	if err != nil {
		return tui.Draft{}, err
	}
	name := defaultName(f.srv, dir)
	d := tui.Draft{
		Server: f.srv, Path: dir,
		Name: name, Alias: config.AliasFor(name),
		Routes: discoveredRoutes(res),
	}
	for _, o := range res.Suggested {
		alias, _ := config.SplitPreset(o.Name)
		reg, _ := cfg.Trusted(alias)
		d.Presets = append(d.Presets, tui.SuggestedPreset{Name: o.Name, Title: offerTitle(o), Repo: reg.Repo})
	}
	advisories, advisoryWarnings := discovery.Advise(res, d.Alias, cfg.Projects)
	for _, a := range advisories {
		d.Advisories = append(d.Advisories, tui.Advisory{Code: a.Code, Message: printableLines(a.Message), Snippet: printableLines(a.Snippet)})
	}
	d.Notes = append(registryNotes(sources), warnings...)
	d.Notes = append(d.Notes, advisoryWarnings...)
	if len(d.Routes) == 0 && res.ComposeUnread() {
		d.Notes = append(d.Notes, "Its Compose file is read only when a preset asks for it with compose: ports.")
	}
	return d, nil
}

func (f *uiFiles) Close() error { return f.files.close() }
