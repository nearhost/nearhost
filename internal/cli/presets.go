package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
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
