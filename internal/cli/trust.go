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
	// nss reads the separate NSS stores; installNSS writes to it
	// alone, without touching the system store.
	nss        func(*x509.Certificate) proxy.NSSStatus
	installNSS func(*x509.Certificate) error
	uninstall  func(*x509.Certificate) error
}

// systemTrust is the production backend. Only NewRoot wires it in.
func systemTrust() trustBackend {
	return trustBackend{
		ensureCA:  proxy.EnsureCA,
		rootCert:  proxy.RootCert,
		isTrusted: proxy.IsTrusted,
		install:   proxy.Install,

		nss:        proxy.CheckNSS,
		installNSS: proxy.InstallNSS,
		uninstall:  proxy.Uninstall,
	}
}

// migrateCA moves the local CA from where Nearhost 0.7 and earlier kept it to
// the data directory, unless another Nearhost is running and may be using it.
// Only the commands that own the CA call it: `nearhost`, `nearhost up`,
// `trust` and `untrust`.
func migrateCA() error { return config.MigrateCaddyDir(nearhostRunning) }

func newTrustCmd(b trustBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "trust",
		Short: "Install the Nearhost Local CA into the system trust store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := migrateCA(); err != nil {
				return err
			}
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
				return trustNSSOnly(out, b, cert)
			}
			fmt.Fprintln(out, "Installing Nearhost Local CA. You may be asked for your password.")
			if err := b.install(cert); err != nil {
				return fmt.Errorf("%w (if you declined the password prompt or the OS refused, run `nearhost trust` again and approve it)", err)
			}
			fmt.Fprintln(out, "Done. Nearhost Local CA is in the system trust store.")
			// The library installs into the NSS stores when it can and skips them
			// silently otherwise, so check what actually happened.
			fmt.Fprintln(out, nssLine(b.nss(cert)))
			return nil
		},
	}
}

// trustNSSOnly handles a root the system store already trusts. That says
// nothing about the separate NSS stores, so check it and, if it lacks the
// root, install there alone (no password prompt, system store untouched).
func trustNSSOnly(out io.Writer, b trustBackend, cert *x509.Certificate) error {
	st := b.nss(cert)
	if st.State == proxy.NSSMissing {
		fmt.Fprintln(out, "Nearhost Local CA is already trusted by the system. Installing it into the "+nssStores+".")
		if err := b.installNSS(cert); err != nil {
			return fmt.Errorf("%w (the system store already trusts the CA; fix the NSS stores and run `nearhost trust` again)", err)
		}
		fmt.Fprintln(out, "Done. The "+nssStores+" now trust the Nearhost Local CA.")
		return nil
	}
	fmt.Fprintln(out, "Nearhost Local CA is already trusted by the system.")
	fmt.Fprintln(out, nssLine(st))
	return nil
}

// nssStores names what the NSS check covers. The library cannot tell a Firefox
// profile from the shared database, so no message may say Firefox alone.
const nssStores = "NSS browser stores (Firefox, and on Linux the shared database Chromium-family browsers use)"

// nssLine describes the NSS stores without claiming more than was seen.
func nssLine(st proxy.NSSStatus) string {
	switch st.State {
	case proxy.NSSCovered:
		return "The " + nssStores + " already trust the Nearhost Local CA."
	case proxy.NSSMissing:
		return "The " + nssStores + " do not trust the Nearhost Local CA yet; run `nearhost trust` again."
	default:
		return "The " + nssStores + " were not configured: " + st.Detail + "."
	}
}

func newUntrustCmd(b trustBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "untrust",
		Short: "Remove the Nearhost Local CA from the system trust store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := migrateCA(); err != nil {
				return err
			}
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
