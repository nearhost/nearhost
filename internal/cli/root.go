// Package cli defines the nearhost command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time by the release tooling.
var Version = "dev"

// NewRoot builds the root command and all subcommands.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "nearhost",
		Short:         "Reach local and remote dev servers at https://<name>.test",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nearhost version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "nearhost %s\n", Version)
			return err
		},
	}
}

// Main runs the CLI with the given arguments and returns the process exit code.
func Main(args []string) int {
	root := NewRoot()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "nearhost:", err)
		return 1
	}
	return 0
}
