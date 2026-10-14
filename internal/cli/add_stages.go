package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/preset"
	"github.com/nearhost/nearhost/internal/transport"
)

// The stages of adding a project or a server. None of them asks or prints:
// `nearhost add` runs them in its order with its line questions, and the
// interface's forms run them as background steps.

// serverFiles is a handle on the files of a server, kept open while a
// project on it is being added.
type serverFiles interface {
	// resolve works out the directory p means and checks that it is one.
	resolve(p string) (string, error)
	// listDirs lists the names of the directories in dir, sorted.
	listDirs(dir string) ([]string, error)
	// reader reads the files of the project in dir.
	reader(dir string) discovery.FileReader
	// close ends the handle.
	close() error
}

// localFiles is the files of this machine.
type localFiles struct{}

func (localFiles) resolve(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", p, err)
	}
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s on this machine: no such directory", abs)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

func (localFiles) listDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
			continue
		}
		// A link to a directory is one to complete into.
		if e.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && info.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func (localFiles) reader(dir string) discovery.FileReader { return discovery.DirReader{Root: dir} }

func (localFiles) close() error { return nil }

// sftpFiles is the files of a server, read over SFTP on a connection of its
// own.
type sftpFiles struct {
	client    *transport.SFTP
	end       func() error // ends the connection
	server    string
	localHome string // this machine's home, for the expanded-home rule
}

func (f *sftpFiles) resolve(p string) (string, error) {
	isDir := func(p string) bool {
		info, err := f.client.Client.Stat(p)
		return err == nil && info.IsDir()
	}
	resolved, err := projectPathOn(p, f.localHome, f.client.Client.Getwd, isDir)
	if err == nil && !path.IsAbs(resolved) {
		// Relative to the home directory, where the SFTP server starts.
		// `nearhost add` refuses relative paths before they get here.
		var home string
		if home, err = f.client.Client.Getwd(); err == nil {
			resolved = path.Join(home, resolved)
		}
	}
	if err != nil {
		return "", fmt.Errorf("find the home directory on server %s: %w", f.server, err)
	}
	resolved = path.Clean(resolved)
	if info, err := f.client.Client.Stat(resolved); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return "", fmt.Errorf("%s on server %s: %w", resolved, f.server, err)
	}
	return resolved, nil
}

func (f *sftpFiles) listDirs(dir string) ([]string, error) {
	entries, err := f.client.Client.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		isDir := e.IsDir()
		if !isDir && e.Mode()&fs.ModeSymlink != 0 {
			info, err := f.client.Client.Stat(path.Join(dir, e.Name()))
			isDir = err == nil && info.IsDir()
		}
		if isDir {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (f *sftpFiles) reader(dir string) discovery.FileReader { return f.client.Reader(dir) }

func (f *sftpFiles) close() error { return errors.Join(f.client.Close(), f.end()) }

// openFiles opens the files of srv: this machine's for the local server, a
// connection of its own for any other.
// then is what to do once access is fixed, for the error of a server that
// refuses the connection.
func openFiles(ctx context.Context, ssh sshClient, srv config.Server, then string) (serverFiles, error) {
	if srv.Name == config.LocalServer {
		return localFiles{}, nil
	}
	return ssh.openServer(ctx, srv, then)
}

// nameTakenError is a project name another project has.
type nameTakenError struct{ other config.Project }

func (e *nameTakenError) Error() string {
	return fmt.Sprintf("a project called %q already exists", e.other.Name)
}

// aliasTakenError is an alias another project has.
type aliasTakenError struct {
	alias string
	other config.Project
}

func (e *aliasTakenError) Error() string {
	return fmt.Sprintf("alias %s is already used by project %q", e.alias, e.other.Name)
}

// errBadName is a name no project can have.
var errBadName = errors.New("a project name cannot be empty, ., ~ or contain a slash")

// nameProblem says why cfg cannot take a project called name, or returns nil.
func nameProblem(cfg *config.Config, name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == "~" {
		return errBadName
	}
	if other, ok := cfg.Project(name); ok {
		return &nameTakenError{other: *other}
	}
	return nil
}

// aliasProblem says why cfg cannot take a project at alias, or returns nil.
// alias is as it will be saved, without blanks around it.
func aliasProblem(cfg *config.Config, alias string) error {
	if _, err := hosts.Rewrite("", []string{alias}); err != nil {
		return err
	}
	for _, other := range cfg.Projects {
		if strings.EqualFold(other.Alias, alias) {
			return &aliasTakenError{alias: alias, other: other}
		}
	}
	return nil
}

// defaultName is the name a project in dir gets unless one is given.
func defaultName(srv config.Server, dir string) string {
	if srv.Name == config.LocalServer {
		return filepath.Base(dir)
	}
	return path.Base(dir)
}

// discoverOnce runs discovery with the enabled presets, offering the presets
// of the trusted registries in sources that are neither enabled nor in
// passed. It returns the result and the warnings of loading the presets and
// of discovery.
func discoverOnce(cfg *config.Config, cfgPath string, reader discovery.FileReader, sources []source, passed map[string]bool) (*discovery.Result, []string, error) {
	// Every registry's presets are offered, so two registries that offer
	// the same name are both offered.
	var offers []discovery.Offer
	for _, s := range sources {
		if s.idx == nil {
			continue
		}
		for _, e := range s.idx.Presets {
			name := s.reg.Alias + "/" + e.Name
			_, enabled := cfg.Preset(name)
			if !enabled && !passed[name] && e.PresetSchema <= preset.SchemaVersion {
				offers = append(offers, discovery.Offer{Name: name, Title: e.Title, Detect: e.Detect})
			}
		}
	}
	presets, found := loadEnabled(cfg, cfgPath)
	res, err := discovery.Discover(reader, discovery.Options{Presets: presets, Offers: offers})
	if err != nil {
		return nil, nil, err
	}
	return res, append(found, res.Warnings...), nil
}

// registryNotes says which trusted registries could not be read.
func registryNotes(sources []source) []string {
	var notes []string
	for _, s := range sources {
		if s.err != nil {
			notes = append(notes, registryProblem(s.reg, s.err)+".")
		}
	}
	return notes
}

// discoveredRoutes is the routes of a discovery result as they are saved,
// with labels made printable.
func discoveredRoutes(res *discovery.Result) []config.Route {
	routes := res.ConfigRoutes()
	// Labels come from presets and the project's own files, and are both
	// printed and saved.
	for i := range routes {
		routes[i].Label = printable(routes[i].Label)
	}
	return routes
}

// saveProject adds project to cfg and saves it. Nothing is saved when the
// project cannot be added.
func saveProject(cfg *config.Config, cfgPath string, project config.Project) error {
	if err := loopbackClash(cfg, project); err != nil {
		return err
	}
	cfg.Projects = append(cfg.Projects, project)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w; nothing was saved", err)
	}
	return config.Save(cfgPath, cfg)
}

// errBadHost is an SSH host that ssh would take for an option, or that is
// not one word.
var errBadHost = errors.New("an SSH host cannot start with - or contain spaces")

// hostProblem says why ssh cannot be given host, or returns nil.
func hostProblem(host string) error {
	if host == "" || strings.HasPrefix(host, "-") || strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return errBadHost
	}
	return nil
}

// serverNameProblem says why cfg cannot take a server called name, or
// returns nil.
func serverNameProblem(cfg *config.Config, name string) error {
	switch {
	case name == "":
		return errors.New("a server needs a name")
	case name == config.LocalServer:
		return fmt.Errorf("the name %q is kept for this machine", config.LocalServer)
	}
	if _, ok := cfg.Server(name); ok {
		return fmt.Errorf("a server called %q already exists", name)
	}
	return nil
}

// addServer adds srv to cfg and saves it.
func addServer(cfg *config.Config, cfgPath string, srv config.Server) error {
	if err := serverNameProblem(cfg, srv.Name); err != nil {
		return err
	}
	if err := hostProblem(srv.SSHHost); err != nil {
		return err
	}
	cfg.Servers = append(cfg.Servers, srv)
	if err := cfg.Validate(); err != nil {
		return err
	}
	return config.Save(cfgPath, cfg)
}

// registryAnswer is an answer to whether to trust the official registry.
type registryAnswer int

const (
	registryYes registryAnswer = iota
	registryNo
	registryNever // don't ask again
)

// answerRegistry does what an answer to the registry question says: trust
// the official registry, saying so on out; nothing; or never ask again. It
// returns a note when the registry could not be trusted, and an error only
// when "don't ask again" could not be saved.
func answerRegistry(ctx context.Context, out io.Writer, b presetBackend, cfg *config.Config, cfgPath string, answer registryAnswer) (string, error) {
	switch answer {
	case registryYes:
		if _, err := b.trust(ctx, out, cfg, cfgPath, officialRepo, ""); err != nil {
			var unread *unreadableRegistryError
			if errors.As(err, &unread) {
				return unread.problem + ", so it was not trusted.", nil
			}
			return strings.TrimRight(err.Error(), ".") + ".", nil
		}
	case registryNever:
		off := false
		cfg.RegistryPrompt = &off
		return "", config.Save(cfgPath, cfg)
	}
	return "", nil
}

// enableOffered enables a preset a registry suggested and saves the config.
// On an error it returns the note to show; a preset that cannot be enabled
// must not stop the add.
func enableOffered(ctx context.Context, b presetBackend, cfg *config.Config, cfgPath, name string) string {
	_, _, err := b.enable(ctx, cfg, cfgPath, name, false)
	if err == nil {
		err = config.Save(cfgPath, cfg)
	}
	if err != nil {
		return fmt.Sprintf("the %s preset was not enabled: %v", name, err)
	}
	return ""
}
