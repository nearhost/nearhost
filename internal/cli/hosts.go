package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/hosts"
)

// newSudoApplier is a variable so tests can avoid real sudo.
var newSudoApplier = func(exe, path string) hosts.Applier { return hosts.SudoApplier(exe, path) }

func newHostsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "Manage the nearhost block in /etc/hosts",
	}
	var file string
	cmd.PersistentFlags().StringVar(&file, "file", hosts.DefaultPath, "hosts file to edit")

	apply := &cobra.Command{
		Use:   "apply",
		Args:  cobra.NoArgs,
		Short: "Set the managed block to the aliases read from stdin (one per line)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var aliases []string
			sc := bufio.NewScanner(cmd.InOrStdin())
			for sc.Scan() {
				if a := strings.TrimSpace(sc.Text()); a != "" {
					aliases = append(aliases, a)
				}
			}
			if err := sc.Err(); err != nil {
				return err
			}
			return hosts.Apply(file, aliases)
		},
	}

	clean := &cobra.Command{
		Use:   "clean",
		Args:  cobra.NoArgs,
		Short: "Remove the managed block (prompts for sudo)",
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			changed, err := hosts.Ensure(file, nil, newSudoApplier(exe, file))
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintln(cmd.OutOrStdout(), "Removed the nearhost block from", file)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "No nearhost block in", file)
			}
			return nil
		},
	}

	cmd.AddCommand(apply, clean)
	return cmd
}
