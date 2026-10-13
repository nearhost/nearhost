package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List configured projects",
		Long: "List the configured projects. STATE is what the config says: an enabled\n" +
			"project starts with `nearhost up`, a disabled one only when named. Whether a\n" +
			"project is being served right now is shown by the `nearhost up` that serves it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.Projects) == 0 {
				_, err := fmt.Fprintln(out, "No projects yet. Add one with `nearhost add <path>`.")
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSERVER\tALIAS\tSTATE\tROUTES")
			for _, p := range cfg.Projects {
				state := "disabled"
				if p.Enabled {
					state = "enabled"
				}
				var routes []string
				for _, r := range p.Routes {
					if r.Role != config.RoleIgnore {
						routes = append(routes, fmt.Sprintf("%s :%d", r.Role, r.Port))
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Server, p.Alias, state, strings.Join(routes, ", "))
			}
			return tw.Flush()
		},
	}
}
