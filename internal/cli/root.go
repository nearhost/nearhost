// Package cli defines the nearhost command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
)

// Version is set at build time by the release tooling.
var Version = "dev"

// NewRoot builds the root command and all subcommands. It is the only place
// where the commands are wired to the real machine.
func NewRoot() *cobra.Command {
	root := newRootCmd()
	root.AddCommand(
		newVersionCmd(),
		newHostsCmd(),
		newTrustCmd(systemTrust()),
		newUntrustCmd(systemTrust()),
		newUpCmd(systemUp()),
		newAddCmd(systemAdd()),
		newLsCmd(),
		newPresetCmd(systemPresets()),
		newDoctorCmd(systemDoctor()),
	)
	bindUI(root, systemUI())
	return root
}

// newRootCmd is the root command with its shared flags and no subcommands.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "nearhost",
		Short:         "Reach local and remote dev servers at https://<name>.test",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "config file (default $XDG_CONFIG_HOME/nearhost/config.yaml, or ~/.config/nearhost/config.yaml when XDG_CONFIG_HOME is not an absolute path)")
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nearhost version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "nearhost %s\n", Version)
			return err
		},
	}
}

// serveFlags are shared by up and doctor.
type serveFlags struct {
	httpsPort int
	httpPort  int
	noHosts   bool
}

func (f *serveFlags) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.httpsPort, "https-port", 443, "port for https://<alias>")
	cmd.Flags().IntVar(&f.httpPort, "http-port", 80, "port that redirects to https")
	cmd.Flags().BoolVar(&f.noHosts, "no-hosts", false, "do not manage /etc/hosts")
}

func (f *serveFlags) validate() error {
	for _, p := range []struct {
		flag string
		port int
	}{{"--https-port", f.httpsPort}, {"--http-port", f.httpPort}} {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535, not %d", p.flag, p.port)
		}
	}
	if f.httpsPort == f.httpPort {
		return fmt.Errorf("--https-port and --http-port must differ (both are %d)", f.httpPort)
	}
	return nil
}

// configPath resolves --config or the default location.
func configPath(cmd *cobra.Command) (string, error) {
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return "", err
	}
	if path != "" {
		return path, nil
	}
	return config.DefaultPath()
}

// loadConfig loads and validates the config and returns it with its path. A
// missing file is an empty config.
func loadConfig(cmd *cobra.Command) (*config.Config, string, error) {
	path, err := configPath(cmd)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, path, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, path, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, path, nil
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
