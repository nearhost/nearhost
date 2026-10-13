package cli

import (
	"crypto/x509"
	"fmt"
	"io"

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
	// firefox reads Firefox's separate NSS store; installFirefox writes to it
	// alone, without touching the system store.
	firefox        func(*x509.Certificate) proxy.FirefoxStatus
	installFirefox func(*x509.Certificate) error
	uninstall      func(*x509.Certificate) error
}

// systemTrust is the production backend. Only NewRoot wires it in.
func systemTrust() trustBackend {
	return trustBackend{
		ensureCA:  proxy.EnsureCA,
		rootCert:  proxy.RootCert,
		isTrusted: proxy.IsTrusted,
		install:   proxy.Install,

		firefox:        proxy.CheckFirefox,
		installFirefox: proxy.InstallFirefox,
		uninstall:      proxy.Uninstall,
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
			out := cmd.OutOrStdout()
			if b.isTrusted(cert) {
				return trustFirefoxOnly(out, b, cert)
			}
			fmt.Fprintln(out, "Installing Nearhost Local CA. You may be asked for your password.")
			if err := b.install(cert); err != nil {
				return fmt.Errorf("%w (if you declined the password prompt or the OS refused, run `nearhost trust` again and approve it)", err)
			}
			fmt.Fprintln(out, "Done. Nearhost Local CA is in the system trust store.")
			// The library installs into Firefox when it can and skips it
			// silently otherwise, so check what actually happened.
			fmt.Fprintln(out, firefoxLine(b.firefox(cert)))
			return nil
		},
	}
}

// trustFirefoxOnly handles a root the system store already trusts. That says
// nothing about Firefox's separate NSS store, so check it and, if it lacks the
// root, install there alone (no password prompt, system store untouched).
func trustFirefoxOnly(out io.Writer, b trustBackend, cert *x509.Certificate) error {
	st := b.firefox(cert)
	if st.State == proxy.FirefoxMissing {
		fmt.Fprintln(out, "Nearhost Local CA is already trusted by the system. Installing it into Firefox.")
		if err := b.installFirefox(cert); err != nil {
			return fmt.Errorf("%w (the system store already trusts the CA; fix Firefox's NSS store and run `nearhost trust` again)", err)
		}
		fmt.Fprintln(out, "Done. Firefox now trusts the Nearhost Local CA.")
		return nil
	}
	fmt.Fprintln(out, "Nearhost Local CA is already trusted by the system.")
	fmt.Fprintln(out, firefoxLine(st))
	return nil
}

// firefoxLine describes the Firefox store without claiming more than was seen.
func firefoxLine(st proxy.FirefoxStatus) string {
	switch st.State {
	case proxy.FirefoxCovered:
		return "Firefox already trusts the Nearhost Local CA."
	case proxy.FirefoxMissing:
		return "Firefox does not trust the Nearhost Local CA yet; run `nearhost trust` again."
	default:
		return "Firefox not configured: " + st.Detail + "."
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
