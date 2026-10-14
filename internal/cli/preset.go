package cli

import (
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
)

func newPresetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preset",
		Short: "Manage presets, which tell nearhost what a framework's ports mean",
		Long: "A preset is a data file that labels a framework's ports, picks their roles\n" +
			"and gives advice about them. No preset is used until it is enabled. Preset\n" +
			"files live in the presets directory beside the config file.",
	}
	cmd.AddCommand(newPresetListCmd(), newPresetEnableCmd(), newPresetDisableCmd(), newPresetValidateCmd())
	return cmd
}

// readLocalPreset parses a preset from the presets directory.
func readLocalPreset(cfgPath, name string) (*preset.Preset, error) {
	data, err := os.ReadFile(presetPath(cfgPath, name))
	if err != nil {
		return nil, err
	}
	return preset.Parse(name, data)
}

// shortRef is a registry commit as people quote it.
func shortRef(ref string) string {
	if len(ref) > 7 {
		return ref[:7]
	}
	return ref
}

func newPresetListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the enabled presets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.Presets) == 0 {
				_, err := fmt.Fprintln(out, "No presets are enabled.")
				return err
			}
			presets, warnings := loadEnabled(cfg, cfgPath)
			titles := map[string]string{}
			for _, p := range presets {
				titles[p.Name] = p.Title
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
			printWarnings(cmd.ErrOrStderr(), warnings)
			return nil
		},
	}
}

func newPresetEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a preset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !preset.ValidName(name) {
				return fmt.Errorf("a preset name is lowercase letters, digits and hyphens, not %q", name)
			}
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			p, err := readLocalPreset(cfgPath, name)
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("no preset named %q: there is no file %s", name, presetPath(cfgPath, name))
			}
			if err != nil {
				return err
			}
			if _, enabled := cfg.Preset(name); enabled {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Preset %s is already enabled.\n", name)
				return err
			}
			cfg.UpsertPreset(config.PresetRef{Name: name})
			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Enabled preset %s (%s).\n", name, p.Title)
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
