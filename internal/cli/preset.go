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
			"and gives advice about them. No preset is used until it is enabled. Preset\n" +
			"files live in the presets directory beside the config file. A preset that\n" +
			"is not there is installed from the registry when it is enabled.",
	}
	cmd.AddCommand(newPresetListCmd(b), newPresetEnableCmd(b), newPresetDisableCmd(), newPresetUpdateCmd(b), newPresetValidateCmd(), newPresetTestCmd(), newPresetIndexCmd())
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
		Short: "List the enabled presets and those the registry offers",
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
					titles[p.Name] = printable(p.Title)
				}
				fmt.Fprintln(out, "Enabled presets:")
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "  NAME\tSOURCE\tTITLE")
				for _, ref := range cfg.Presets {
					source := "local"
					if ref.Ref != "" {
						source = "registry " + shortRef(ref.Ref)
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

			idx, err := b.index(cmd.Context(), cfg, cfgPath)
			switch {
			case errors.Is(err, errRegistryOff):
				// Nothing to add: the user knows the registry is off.
			case err != nil:
				fmt.Fprintln(out, "\nThe registry could not be reached, so the presets it offers are not listed.")
			default:
				var offered []registry.Entry
				for _, e := range idx.Presets {
					if _, enabled := cfg.Preset(e.Name); !enabled {
						offered = append(offered, e)
					}
				}
				if len(offered) > 0 {
					fmt.Fprintln(out, "\nAvailable from the registry:")
					tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
					fmt.Fprintln(tw, "  NAME\tTITLE")
					for _, e := range offered {
						fmt.Fprintf(tw, "  %s\t%s\n", e.Name, printable(e.Title))
					}
					if err := tw.Flush(); err != nil {
						return err
					}
					fmt.Fprintln(out, "\nEnable one with `nearhost preset enable <name>`.")
				}
			}
			printWarnings(cmd.ErrOrStderr(), warnings)
			return nil
		},
	}
}

func newPresetEnableCmd(b presetBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a preset, installing it from the registry if it is not a local file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			p, changed, err := b.enable(cmd.Context(), cfg, cfgPath, name)
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
		Use:   "update [name]",
		Short: "Move presets installed from the registry to its current version",
		Long: "Installed presets never change on their own. This fetches the registry's\n" +
			"current version of the named preset, or of every preset installed from the\n" +
			"registry, and says what changed. A preset file edited by hand is kept\n" +
			"unless --force is given.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			var targets []config.PresetRef
			if len(args) == 1 {
				ref, ok := cfg.Preset(args[0])
				switch {
				case !ok:
					return fmt.Errorf("preset %q is not enabled", args[0])
				case ref.Ref == "":
					return fmt.Errorf("preset %q is a local file, not one from the registry, so there is nothing to update it from", args[0])
				}
				targets = []config.PresetRef{ref}
			} else {
				for _, ref := range cfg.Presets {
					if ref.Ref != "" {
						targets = append(targets, ref)
					}
				}
			}
			out := cmd.OutOrStdout()
			if len(targets) == 0 {
				_, err := fmt.Fprintln(out, "No presets from the registry are enabled.")
				return err
			}
			if b.registry == nil || !cfg.RegistryEnabled() {
				return errRegistryOff
			}
			// An update is a request for what is there now, so the cached
			// index is not good enough.
			idx, err := b.registry(cfgPath).Refresh(cmd.Context())
			if err != nil {
				return fmt.Errorf("the registry could not be reached: %w", err)
			}
			changed, failed := false, 0
			for _, ref := range targets {
				line, did, err := b.update(cmd.Context(), cfg, cfgPath, idx, ref, force)
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

// update moves one registry preset to the index's version. It returns what
// happened in words and whether cfg changed.
func (b presetBackend) update(ctx context.Context, cfg *config.Config, cfgPath string, idx *registry.Index, ref config.PresetRef, force bool) (string, bool, error) {
	e, listed := idx.Entry(ref.Name)
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
	if _, err := b.install(ctx, cfg, cfgPath, idx, ref.Name); err != nil {
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
