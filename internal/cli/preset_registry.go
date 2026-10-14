package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
)

// noRegistriesHint is shown where registry presets would be while no
// registry is trusted. It is local text: nothing is sent anywhere.
const noRegistriesHint = "No preset registries are trusted. To use the official presets: nearhost preset registry add nearhost/presets"

func newPresetRegistryCmd(b presetBackend) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage the preset registries you trust",
		Long: "A preset registry is a public GitHub repository of presets. No registry is\n" +
			"trusted until you add it, and until then nearhost asks no registry for\n" +
			"anything. Presets from a registry are named <registry>/<name>.",
	}
	cmd.AddCommand(newRegistryAddCmd(b), newRegistryListCmd(b), newRegistryRemoveCmd())
	return cmd
}

func newRegistryAddCmd(b presetBackend) *cobra.Command {
	var as string
	cmd := &cobra.Command{
		Use:   "add <owner/repo>",
		Short: "Trust the preset registry in a public GitHub repository",
		Long: "Fetches the repository's index once and, if it is valid, saves the registry.\n" +
			"Its presets are then named <alias>/<name>. The alias is the repository's\n" +
			"owner in lower case unless --as gives another.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			reg, err := newRegistry(cfg, args[0], as)
			if err != nil {
				return err
			}
			idx, err := b.client(cfgPath, reg).Refresh(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s; nothing was saved", registryProblem(reg, err))
			}
			cfg.Registries = append(cfg.Registries, reg)
			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Trusted registry %s (%s). Its index at %s offers %s.\n"+
				"Enable one with `nearhost preset enable %s/<name>`; `nearhost preset list` shows them.\n",
				reg.Alias, reg.Repo, shortRef(idx.Ref), plural(len(idx.Presets), "preset"), reg.Alias)
			return err
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "the alias its presets are named under (default: the owner in lower case)")
	return cmd
}

// newRegistry checks a repo and alias given to `registry add` and returns the
// registry they make. Nothing is fetched.
func newRegistry(cfg *config.Config, repo, alias string) (config.Registry, error) {
	fail := func(format string, a ...any) (config.Registry, error) {
		return config.Registry{}, fmt.Errorf(format, a...)
	}
	if !config.ValidRepo(repo) {
		return fail("%q is not a GitHub repository; write it as owner/name", repo)
	}
	if alias == "" {
		owner, _, _ := strings.Cut(repo, "/")
		alias = strings.ToLower(owner)
		if !config.ValidAlias(alias) {
			return fail("the owner %q does not make an alias, which is lowercase letters and digits with single hyphens between words; pass one with --as", owner)
		}
	} else if !config.ValidAlias(alias) {
		return fail("alias %q must be lowercase letters and digits, with single hyphens between words", alias)
	}
	for _, r := range cfg.Registries {
		switch {
		case strings.EqualFold(r.Repo, repo):
			return fail("%s is already trusted as %q", r.Repo, r.Alias)
		case r.Alias == alias:
			return fail("a registry is already trusted as %q (%s); pass another alias with --as", alias, r.Repo)
		}
	}
	return config.Registry{Alias: alias, Repo: repo}, nil
}

func newRegistryListCmd(b presetBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the trusted preset registries and their cached indexes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.Registries) == 0 {
				_, err := fmt.Fprintln(out, noRegistriesHint)
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ALIAS\tREPO\tINDEX")
			for _, reg := range cfg.Registries {
				index := "not fetched"
				if idx, age, err := b.client(cfgPath, reg).Cached(); err == nil {
					index = fmt.Sprintf("%s, fetched %s", shortRef(idx.Ref), ago(age))
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", reg.Alias, reg.Repo, index)
			}
			return tw.Flush()
		},
	}
}

func newRegistryRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <alias>",
		Short: "Stop trusting a preset registry; presets installed from it are kept",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := args[0]
			cfg, cfgPath, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			// Only an alias the config holds, and so a valid one, reaches a
			// path below.
			reg, ok := cfg.Trusted(alias)
			if !ok {
				return fmt.Errorf("no registry is trusted as %q", alias)
			}
			var enabled []string
			for _, p := range cfg.Presets {
				if a, _ := config.SplitPreset(p.Name); a == alias {
					enabled = append(enabled, p.Name)
				}
			}
			if len(enabled) > 0 {
				return fmt.Errorf("presets from registry %s are enabled: %s. Disable them first with `nearhost preset disable <name>`", alias, strings.Join(enabled, ", "))
			}
			cfg.Registries = slices.DeleteFunc(cfg.Registries, func(r config.Registry) bool { return r.Alias == alias })
			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}
			if err := os.RemoveAll(registryDir(cfgPath, alias)); err != nil {
				return err
			}
			msg := fmt.Sprintf("Removed registry %s (%s).", alias, reg.Repo)
			if dir := filepath.Join(presetsDir(cfgPath), alias); exists(dir) {
				msg += fmt.Sprintf(" The presets installed from it are kept in %s.", dir)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
			return err
		},
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ago says how long ago something happened, in the largest whole unit.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	}
	return plural(int(d/(24*time.Hour)), "day") + " ago"
}
