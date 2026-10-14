package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
	"github.com/nearhost/nearhost/internal/registry"
)

func newPresetCmd(b presetBackend) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preset",
		Short: "Manage presets, which tell nearhost what a framework's ports mean",
		Long: "A preset is a data file that labels a framework's ports, picks their roles\n" +
			"and gives advice about them. No preset is used until it is enabled. Local\n" +
			"preset files live in the presets directory beside the config file. A preset\n" +
			"from a trusted registry is named <registry>/<name> and is installed when it\n" +
			"is enabled.",
	}
	cmd.AddCommand(newPresetListCmd(b), newPresetEnableCmd(b), newPresetDisableCmd(), newPresetUpdateCmd(b), newPresetRegistryCmd(b), newPresetValidateCmd(), newPresetTestCmd(), newPresetIndexCmd())
	return cmd
}

// shortRef is a registry commit as people quote it.
func shortRef(ref string) string {
	if len(ref) > 7 {
		return ref[:7]
	}
	return ref
}

func newPresetListCmd(b presetBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the enabled presets and those the trusted registries offer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var warnings []string
			if len(cfg.Presets) == 0 {
				fmt.Fprintln(out, "No presets are enabled.")
			} else {
				var presets []*preset.Preset
				presets, warnings = loadEnabled(cfg, cfgPath)
				titles := map[string]string{}
				for _, p := range presets {
					titles[p.Key()] = printable(p.Title)
				}
				fmt.Fprintln(out, "Enabled presets:")
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "  NAME\tSOURCE\tTITLE")
				for _, ref := range cfg.Presets {
					source := "local"
					if alias, _ := config.SplitPreset(ref.Name); alias != "" {
						source = alias + " @ " + shortRef(ref.Ref)
					}
					title, ok := titles[ref.Name]
					if !ok {
						title = "(cannot be used)"
					}
					fmt.Fprintf(tw, "  %s\t%s\t%s\n", ref.Name, source, title)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
			}

			if len(cfg.Registries) == 0 {
				fmt.Fprintf(out, "\n%s\n", noRegistriesHint)
			}

			offered := false
			for _, s := range b.indexes(cmd.Context(), cfg, cfgPath) {
				if s.err != nil {
					fmt.Fprintf(out, "\nWarning: %s.\n", registryProblem(s.reg, s.err))
					continue
				}
				var entries []registry.Entry
				for _, e := range s.idx.Presets {
					if _, enabled := cfg.Preset(s.reg.Alias + "/" + e.Name); !enabled {
						entries = append(entries, e)
					}
				}
				if len(entries) == 0 {
					continue
				}
				offered = true
				fmt.Fprintf(out, "\nAvailable from %s (%s):\n", s.reg.Alias, s.reg.Repo)
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "  NAME\tTITLE")
				for _, e := range entries {
					fmt.Fprintf(tw, "  %s/%s\t%s\n", s.reg.Alias, e.Name, printable(e.Title))
				}
				if err := tw.Flush(); err != nil {
					return err
				}
			}
			if offered {
				fmt.Fprintln(out, "\nEnable one with `nearhost preset enable <registry>/<name>`.")
			}
			printWarnings(cmd.ErrOrStderr(), warnings)
			return nil
		},
	}
}

func newPresetEnableCmd(b presetBackend) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a local preset, or install one from a trusted registry as <registry>/<name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			p, changed, err := b.enable(cmd.Context(), cfg, cfgPath, name, force)
			if err != nil {
				return err
			}
			if !changed {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Preset %s is already enabled.\n", name)
				return err
			}
			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Enabled preset %s (%s).\n", name, printable(p.Title))
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace a kept file that differs from the registry's current version")
	return cmd
}

func newPresetDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Stop using a preset; its file is kept",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if !cfg.RemovePreset(name) {
				return fmt.Errorf("preset %q is not enabled", name)
			}
			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Disabled preset %s. Its file is kept at %s.\n", name, presetPath(cfgPath, name))
			return err
		},
	}
}

func newPresetUpdateCmd(b presetBackend) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "update [name|registry]",
		Short: "Move presets installed from registries to their current versions",
		Long: "Installed presets never change on their own. This fetches the current index\n" +
			"of every registry with enabled presets, or of the one named, and moves the\n" +
			"named preset, or every preset from those registries, to the registry's\n" +
			"current version, saying what changed. A preset file edited by hand is kept\n" +
			"unless --force is given.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			targets, err := updateTargets(cfg, args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(targets) == 0 {
				what := "a registry"
				if len(args) == 1 {
					what = "registry " + args[0]
				}
				_, err := fmt.Fprintf(out, "No presets from %s are enabled.\n", what)
				return err
			}
			// An update is a request for what is there now, so a cached index
			// is not good enough. Each registry is asked once.
			indexes := map[string]*registry.Index{}
			problems := map[string]error{}
			changed, failed := false, 0
			for _, ref := range targets {
				alias, _ := config.SplitPreset(ref.Name)
				reg, _ := cfg.Trusted(alias) // the config is valid, so it is there
				if indexes[alias] == nil && problems[alias] == nil {
					indexes[alias], problems[alias] = b.client(cfgPath, reg).Refresh(cmd.Context())
				}
				if err := problems[alias]; err != nil {
					failed++
					fmt.Fprintf(out, "%s: not updated: %s\n", ref.Name, registryProblem(reg, err))
					continue
				}
				line, did, err := b.update(cmd.Context(), cfg, cfgPath, reg, indexes[alias], ref, force)
				if err != nil {
					failed++
					fmt.Fprintf(out, "%s: not updated: %v\n", ref.Name, err)
					continue
				}
				changed = changed || did
				fmt.Fprintf(out, "%s: %s\n", ref.Name, line)
			}
			if changed {
				if err := config.Save(cfgPath, cfg); err != nil {
					return err
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d presets could not be updated", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite a preset file that was edited by hand")
	return cmd
}

// updateTargets is what `preset update` works on: the enabled preset named,
// every enabled preset from the registry named, or with no name every
// enabled preset from a registry.
func updateTargets(cfg *config.Config, args []string) ([]config.PresetRef, error) {
	from := func(alias string) []config.PresetRef {
		var targets []config.PresetRef
		for _, ref := range cfg.Presets {
			if a, _ := config.SplitPreset(ref.Name); a != "" && (alias == "" || a == alias) {
				targets = append(targets, ref)
			}
		}
		return targets
	}
	if len(args) == 0 {
		return from(""), nil
	}
	name := args[0]
	if _, ok := cfg.Trusted(name); ok {
		return from(name), nil
	}
	ref, ok := cfg.Preset(name)
	switch alias, _ := config.SplitPreset(name); {
	case !ok && strings.Contains(name, "/"):
		// An alias never holds a slash, so no registry could be meant.
		return nil, fmt.Errorf("preset %q is not enabled", name)
	case !ok:
		return nil, fmt.Errorf("no preset named %q is enabled and no registry is trusted as %q", name, name)
	case alias == "":
		return nil, fmt.Errorf("preset %q is a local file, not one from a registry, so there is nothing to update it from", name)
	}
	return []config.PresetRef{ref}, nil
}

// update moves one registry preset to the version in idx, the index of reg.
// It returns what happened in words and whether cfg changed.
func (b presetBackend) update(ctx context.Context, cfg *config.Config, cfgPath string, reg config.Registry, idx *registry.Index, ref config.PresetRef, force bool) (string, bool, error) {
	_, plain := config.SplitPreset(ref.Name)
	e, listed := idx.Entry(plain)
	if !listed {
		return "kept; the registry no longer lists it", false, nil
	}
	data, err := os.ReadFile(presetPath(cfgPath, ref.Name))
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return "", false, err
	}
	edited := !missing && registry.Sum(data) != ref.SHA256
	switch {
	case edited && !force:
		return fmt.Sprintf("kept; the file has local edits. Run `nearhost preset update %s --force` to overwrite them", ref.Name), false, nil
	case !missing && !edited && e.SHA256 == ref.SHA256:
		return "up to date", false, nil
	}
	if _, err := b.install(ctx, cfg, cfgPath, reg, idx, plain); err != nil {
		return "", false, err
	}
	switch {
	case missing:
		return "installed again at " + shortRef(idx.Ref), true, nil
	case edited:
		return "overwritten with the registry's version at " + shortRef(idx.Ref), true, nil
	}
	return fmt.Sprintf("updated from %s to %s", shortRef(ref.Ref), shortRef(idx.Ref)), true, nil
}

func newPresetValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file>",
		Short: "Check a preset file and report every problem in it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := args[0]
			if filepath.Ext(file) != ".yaml" {
				return fmt.Errorf("%s: a preset file is named <name>.yaml", file)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if _, err := preset.Parse(strings.TrimSuffix(filepath.Base(file), ".yaml"), data); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s is valid.\n", file)
			return err
		},
	}
}
