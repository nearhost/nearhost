package cli

import (
	"crypto/x509"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
)

// trustBackend is the seam between the trust commands and the machine: the
// real one touches the system trust store, tests substitute a fake.
type trustBackend struct {
	ensureCA  func(storageRoot string) (*x509.Certificate, error)
	rootCert  func(storageRoot string) (*x509.Certificate, error)
	isTrusted func(*x509.Certificate) bool
	install   func(*x509.Certificate) error
	uninstall func(*x509.Certificate) error
}

// systemTrust is the production backend. Only NewRoot wires it in.
func systemTrust() trustBackend {
	return trustBackend{
		ensureCA:  proxy.EnsureCA,
		rootCert:  proxy.RootCert,
		isTrusted: proxy.IsTrusted,
		install:   proxy.Install,
		uninstall: proxy.Uninstall,
	}
}

func newTrustCmd(b trustBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "trust",
		Short: "Install the Nearhost Local CA into the system trust store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.CaddyDir()
			if err != nil {
				return err
			}
			cert, err := b.ensureCA(dir)
			if err != nil {
				return err
			}
			if b.isTrusted(cert) {
				fmt.Fprintln(cmd.OutOrStdout(), "Nearhost Local CA is already trusted.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Installing Nearhost Local CA. You may be asked for your password.")
			if err := b.install(cert); err != nil {
				return fmt.Errorf("%w (if you declined the password prompt or the OS refused, run `nearhost trust` again and approve it)", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Done. Browsers now trust https://<alias>.test sites served by nearhost.")
			return nil
		},
	}
}

func newUntrustCmd(b trustBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "untrust",
		Short: "Remove the Nearhost Local CA from the system trust store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.CaddyDir()
			if err != nil {
				return err
			}
			cert, err := b.rootCert(dir)
			if err != nil {
				return fmt.Errorf("no local CA found: %w", err)
			}
			if err := b.uninstall(cert); err != nil {
				return fmt.Errorf("%w (if you declined the password prompt or the OS refused, run `nearhost untrust` again and approve it)", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Nearhost Local CA removed from the trust store.")
			return nil
		},
	}
}
