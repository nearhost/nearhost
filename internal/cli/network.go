package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/report"
)

func newNetworkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage how Nearhost serves to other devices",
		Args:  cobra.NoArgs,
		RunE:  groupRun,
	}
	var clearToken bool
	token := &cobra.Command{
		Use:   "token",
		Short: "Save the DNS provider's API token for the network's certificate",
		Long: "Read the DNS provider's API token from the terminal, without showing it, or\n" +
			"from a pipe, and save it where only you can read it. Nearhost uses it to get\n" +
			"a certificate for the network domain. A running Nearhost reloads at once.\n" +
			"With --clear it removes the saved token.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNetworkToken(cmd, clearToken)
		},
	}
	token.Flags().BoolVar(&clearToken, "clear", false, "remove the saved token")
	cmd.AddCommand(token)
	return cmd
}

func runNetworkToken(cmd *cobra.Command, clearToken bool) error {
	path, err := config.DNSTokenPath()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if clearToken {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		fmt.Fprintln(out, "Removed the DNS token.")
		return reloadCore(cmd, out)
	}
	tok, err := readSecret(cmd)
	if err != nil {
		return err
	}
	if tok == "" {
		return report.Errorf(report.CLIUsage, "No token was given, so nothing was saved.")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path, []byte(tok), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(out, "Saved the DNS token.")
	return reloadCore(cmd, out)
}

// reloadCore has a running core read the config and token again, as a core
// that runs reloads. None running is no failure.
func reloadCore(cmd *cobra.Command, out io.Writer) error {
	c, err := dialCore()
	if errors.Is(err, control.ErrNotRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := c.Reload(orBackground(cmd.Context())); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Nearhost reloaded.")
	return err
}

// readSecret reads one line from the command's input, without echo when
// that is a terminal, and trims it.
func readSecret(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.OutOrStdout(), "DNS provider token: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		return strings.TrimSpace(string(b)), err
	}
	b, err := io.ReadAll(io.LimitReader(in, 64<<10))
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line), nil
}
