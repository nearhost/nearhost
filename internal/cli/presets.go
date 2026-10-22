package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
	"github.com/nearhost/nearhost/internal/registry"
)

// presetsDir is where preset files live: beside the config file in use.
func presetsDir(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "presets")
}

// presetPath is where the enabled preset of the given name is kept: a local
// preset in presets/<name>.yaml, one from a registry in
// presets/<alias>/<name>.yaml. The name must already be valid, so that it
// cannot lead out of its directory.
func presetPath(cfgPath, name string) string {
	alias, plain := config.SplitPreset(name)
	return filepath.Join(presetsDir(cfgPath), alias, plain+".yaml")
}

// registryDir is where the index of the registry with the given alias is
// cached.
func registryDir(cfgPath, alias string) string {
	return filepath.Join(filepath.Dir(cfgPath), "registry", alias)
}

// parsePreset parses the enabled preset of the given name and records which
// registry, if any, it came from.
func parsePreset(name string, data []byte) (*preset.Preset, error) {
	alias, plain := config.SplitPreset(name)
	p, err := preset.Parse(plain, data)
	if err != nil {
		return nil, err
	}
	p.Source = alias
	return p, nil
}

// loadEnabled parses every enabled preset. One that cannot be used is left
// out and reported, so that a broken preset never blocks a command.
func loadEnabled(cfg *config.Config, cfgPath string) ([]*preset.Preset, []string) {
	var presets []*preset.Preset
	var warnings []string
	for _, ref := range cfg.Presets {
		file := presetPath(cfgPath, ref.Name)
		data, err := os.ReadFile(file)
		switch {
		case errors.Is(err, fs.ErrNotExist) && ref.Ref != "":
			warnings = append(warnings, fmt.Sprintf("preset %q is enabled but %s is missing; run `nearhost preset enable %s` to install it again", ref.Name, file, ref.Name))
			continue
		case errors.Is(err, fs.ErrNotExist):
			warnings = append(warnings, fmt.Sprintf("preset %q is enabled but %s is missing; put the file back or run `nearhost preset disable %s`", ref.Name, file, ref.Name))
			continue
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("preset %q skipped: %v", ref.Name, err))
			continue
		}
		p, err := parsePreset(ref.Name, data)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("preset %q skipped: %v", ref.Name, err))
			continue
		}
		presets = append(presets, p)
	}
	return presets, warnings
}

func printWarnings(w io.Writer, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "Warning: %s\n", warning)
	}
}

// presetBackend is the seam between the commands and the preset registries.
type presetBackend struct {
	// registry returns the client for a trusted registry, with its index
	// cached in dir.
	registry func(reg config.Registry, dir string) *registry.Client
}

// systemPresets is the production backend. Only NewRoot wires it in.
func systemPresets() presetBackend {
	return presetBackend{registry: func(reg config.Registry, dir string) *registry.Client {
		return registry.ForRepo(reg.Repo, dir)
	}}
}

// client returns the client for reg, with its cache beside the config file.
func (b presetBackend) client(cfgPath string, reg config.Registry) *registry.Client {
	return b.registry(reg, registryDir(cfgPath, reg.Alias))
}

// source is a trusted registry and what reading its index gave.
type source struct {
	reg config.Registry
	idx *registry.Index
	err error
}

// indexes reads the index of every trusted registry, from its cache while
// that is under a day old. The registries are read at the same time, so a
// slow one holds the others up only as long as its own timeout. With no
// registry trusted, no request is made.
func (b presetBackend) indexes(ctx context.Context, cfg *config.Config, cfgPath string) []source {
	sources := make([]source, len(cfg.Registries))
	var wg sync.WaitGroup
	for i, reg := range cfg.Registries {
		sources[i].reg = reg
		wg.Go(func() { sources[i].idx, sources[i].err = b.client(cfgPath, reg).Index(ctx) })
	}
	wg.Wait()
	return sources
}

// registryProblem says why a registry's index cannot be used, in the words
// every command uses.
func registryProblem(reg config.Registry, err error) string {
	if errors.Is(err, registry.ErrNewerIndex) {
		return fmt.Sprintf("registry %s (%s) needs a newer nearhost than this one; upgrade nearhost to use it", reg.Alias, reg.Repo)
	}
	return fmt.Sprintf("registry %s (%s) cannot be read: %s", reg.Alias, reg.Repo, printable(err.Error()))
}

// enable makes name an enabled preset in cfg and reports whether cfg
// changed. It does not save cfg. A plain name is a local file. A qualified
// name is installed from its registry, unless the file kept from an earlier
// install is the registry's current version. A kept file that differs from
// it is replaced only with force.
func (b presetBackend) enable(ctx context.Context, cfg *config.Config, cfgPath, name string, force bool) (*preset.Preset, bool, error) {
	if !config.ValidPresetName(name) {
		return nil, false, fmt.Errorf("a preset name is lowercase letters, digits and hyphens, or <registry>/<name> for a preset from a registry, not %q", name)
	}
	alias, plain := config.SplitPreset(name)
	if alias == "" {
		return enableLocal(cfg, cfgPath, name)
	}
	reg, ok := cfg.Trusted(alias)
	if !ok {
		return nil, false, fmt.Errorf("no registry is trusted as %q. Add one with: nearhost preset registry add <owner/repo> --as %s", alias, alias)
	}
	file := presetPath(cfgPath, name)
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	kept := err == nil
	if _, enabled := cfg.Preset(name); enabled && kept {
		p, err := parsePreset(name, data)
		return p, false, err
	}

	idx, err := b.client(cfgPath, reg).Index(ctx)
	if err != nil {
		return nil, false, errors.New(registryProblem(reg, err))
	}
	e, ok := idx.Entry(plain)
	if !ok {
		return nil, false, fmt.Errorf("no preset named %q: registry %s (%s) does not list %s", name, alias, reg.Repo, plain)
	}
	switch {
	case kept && registry.Sum(data) == e.SHA256:
		p, err := parsePreset(name, data)
		if err != nil {
			return nil, false, err
		}
		cfg.UpsertPreset(config.PresetRef{Name: name, Ref: idx.Ref, SHA256: e.SHA256})
		return p, true, nil
	case kept && !force:
		return nil, false, fmt.Errorf("%s is kept from an earlier install and differs from the version registry %s offers now, because the registry has moved on or the file was edited. Run `nearhost preset enable %s --force` to replace it with the registry's version", file, alias, name)
	}
	p, err := b.install(ctx, cfg, cfgPath, reg, idx, plain)
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// savePresets records the named presets in the config at cfgPath as cfg has
// them, under the config's lock, and changes nothing else in the file: cfg
// was loaded before the presets were fetched, and the file may have changed
// since.
func savePresets(cfgPath string, cfg *config.Config, names ...string) error {
	var refs []config.PresetRef
	for _, name := range names {
		if ref, ok := cfg.Preset(name); ok {
			refs = append(refs, ref)
		}
	}
	return config.Update(cfgPath, func(cfg *config.Config) error {
		for _, ref := range refs {
			cfg.UpsertPreset(ref)
		}
		return nil
	})
}

// enableLocal enables the local preset file of the given plain name. No
// registry is asked about it.
func enableLocal(cfg *config.Config, cfgPath, name string) (*preset.Preset, bool, error) {
	file := presetPath(cfgPath, name)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, fmt.Errorf("no preset named %q: there is no file %s. A preset from a registry is named <registry>/<name>", name, file)
	}
	if err != nil {
		return nil, false, err
	}
	p, err := parsePreset(name, data)
	if err != nil {
		return nil, false, err
	}
	if _, enabled := cfg.Preset(name); enabled {
		return p, false, nil
	}
	cfg.UpsertPreset(config.PresetRef{Name: name})
	return p, true, nil
}

// install downloads the preset plain from reg, writes it to the registry's
// own presets directory and records it in cfg. Nothing is written unless the
// download matches the index and parses.
func (b presetBackend) install(ctx context.Context, cfg *config.Config, cfgPath string, reg config.Registry, idx *registry.Index, plain string) (*preset.Preset, error) {
	name := reg.Alias + "/" + plain
	e, _ := idx.Entry(plain)
	if e.PresetSchema > preset.SchemaVersion {
		return nil, fmt.Errorf("preset %s needs a newer nearhost: its schema is %d and this nearhost understands up to %d", name, e.PresetSchema, preset.SchemaVersion)
	}
	data, err := b.client(cfgPath, reg).Fetch(ctx, idx, plain)
	if err != nil {
		return nil, err
	}
	p, err := parsePreset(name, data)
	if err != nil {
		return nil, fmt.Errorf("the registry's preset cannot be used: %s", printable(err.Error()))
	}
	file := presetPath(cfgPath, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return nil, err
	}
	cfg.UpsertPreset(config.PresetRef{Name: name, Ref: idx.Ref, SHA256: e.SHA256})
	return p, nil
}

// printable makes text from a preset or the registry safe to print: one line, no control characters.
func printable(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s))
}

// printableLines makes advice safe to print. Unlike printable it keeps line
// breaks, tabs and indentation, which a snippet may need, and drops the
// other characters printable would.
func printableLines(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
}
