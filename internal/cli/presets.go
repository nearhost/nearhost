package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
	"github.com/nearhost/nearhost/internal/registry"
)

// presetsDir is where preset files live: beside the config file in use.
func presetsDir(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "presets")
}

func presetPath(cfgPath, name string) string {
	return filepath.Join(presetsDir(cfgPath), name+".yaml")
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
		p, err := preset.Parse(ref.Name, data)
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

// presetBackend is the seam between the commands and the preset registry.
type presetBackend struct {
	// registry returns the client for the registry, with its cache beside
	// the config file. Nil means there is no registry.
	registry func(cfgPath string) *registry.Client
}

// systemPresets is the production backend. Only NewRoot wires it in.
func systemPresets() presetBackend {
	return presetBackend{registry: func(cfgPath string) *registry.Client {
		return registry.New(filepath.Join(filepath.Dir(cfgPath), "registry"))
	}}
}

var errRegistryOff = errors.New("the preset registry is switched off (`registry: false` in the config)")

// index returns the registry index, cached for a day. No request is made
// when the config switches the registry off.
func (b presetBackend) index(ctx context.Context, cfg *config.Config, cfgPath string) (*registry.Index, error) {
	if b.registry == nil || !cfg.RegistryEnabled() {
		return nil, errRegistryOff
	}
	return b.registry(cfgPath).Index(ctx)
}

// enable makes name an enabled preset in cfg: the file in the presets
// directory when there is one, else a copy installed from the registry. It
// reports whether cfg changed and does not save it.
func (b presetBackend) enable(ctx context.Context, cfg *config.Config, cfgPath, name string) (*preset.Preset, bool, error) {
	if !preset.ValidName(name) {
		return nil, false, fmt.Errorf("a preset name is lowercase letters, digits and hyphens, not %q", name)
	}
	file := presetPath(cfgPath, name)
	data, err := os.ReadFile(file)
	if err == nil {
		p, err := preset.Parse(name, data)
		if err != nil {
			return nil, false, err
		}
		if _, enabled := cfg.Preset(name); enabled {
			return p, false, nil
		}
		ref := config.PresetRef{Name: name}
		// A file the registry still describes exactly is its preset, kept
		// from an earlier install, and stays one.
		if idx, err := b.index(ctx, cfg, cfgPath); err == nil {
			if e, ok := idx.Entry(name); ok && e.SHA256 == registry.Sum(data) {
				ref.Ref, ref.SHA256 = idx.Ref, e.SHA256
			}
		}
		cfg.UpsertPreset(ref)
		return p, true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}

	idx, err := b.index(ctx, cfg, cfgPath)
	if err != nil {
		return nil, false, fmt.Errorf("no preset named %q: there is no file %s, and %w", name, file, err)
	}
	if _, ok := idx.Entry(name); !ok {
		return nil, false, fmt.Errorf("no preset named %q: there is no file %s and the registry does not list it", name, file)
	}
	p, err := b.install(ctx, cfg, cfgPath, idx, name)
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// install downloads name from the registry, writes it to the presets
// directory and records it in cfg. Nothing is written unless the download
// matches the index and parses.
func (b presetBackend) install(ctx context.Context, cfg *config.Config, cfgPath string, idx *registry.Index, name string) (*preset.Preset, error) {
	e, _ := idx.Entry(name)
	if e.PresetSchema > preset.SchemaVersion {
		return nil, fmt.Errorf("preset %s needs a newer nearhost: its schema is %d and this nearhost understands up to %d", name, e.PresetSchema, preset.SchemaVersion)
	}
	data, err := b.registry(cfgPath).Fetch(ctx, idx, name)
	if err != nil {
		return nil, err
	}
	p, err := preset.Parse(name, data)
	if err != nil {
		return nil, fmt.Errorf("the registry's preset cannot be used: %w", err)
	}
	if err := os.MkdirAll(presetsDir(cfgPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(presetPath(cfgPath, name), data, 0o644); err != nil {
		return nil, err
	}
	cfg.UpsertPreset(config.PresetRef{Name: name, Ref: idx.Ref, SHA256: e.SHA256})
	return p, nil
}
