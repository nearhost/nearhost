package cli

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/smallstep/truststore"
	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/sudo"
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
	// uninstallNSS removes the root from the NSS stores alone, without
	// touching the system store. nil skips it.
	uninstallNSS func(*x509.Certificate) error
	// findSudo returns sudo.ErrNotFound when the system store cannot be
	// changed: no root-owned sudo, and Nearhost is not root. nil skips the
	// check.
	findSudo func() error
}

// noSudo says what to do about a missing sudo, for the command name.
func noSudo(err error, name string) error {
	return fmt.Errorf("%w; install sudo in one of those, or run `nearhost %s` as root", err, name)
}

// sudoProblem is the backend's findSudo for the command name, worded by
// noSudo, or nil.
func (b trustBackend) sudoProblem(name string) error {
	if b.findSudo == nil {
		return nil
	}
	if err := b.findSudo(); err != nil {
		return noSudo(err, name)
	}
	return nil
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

		uninstallNSS: proxy.UninstallNSS,
		findSudo:     systemSudo,
	}
}

// migrateCA moves the local CA from where Nearhost 0.7 and earlier kept it to
// the data directory, unless another Nearhost is running and may be using it.
// Only the commands that own the CA call it: `nearhost`, `nearhost up`,
// `trust` and `untrust`. A failure leaves a CA where config.CaddyDir finds
// it, so it is a warning on w and the command carries on.
func migrateCA(w io.Writer) {
	if msg := migrateCANote(); msg != "" {
		fmt.Fprintln(w, msg)
	}
}

// migrateCANote is migrateCA for the interface, which hides stderr while it
// runs: it returns the warning, "" for none, for the interface's log.
func migrateCANote() string {
	return migrateWarning(config.MigrateCaddyDir(nearhostRunning))
}

// migrateWarning words a config.MigrateCaddyDir error for the user, "" for
// none.
func migrateWarning(err error) string {
	var me *config.MigrateError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &me) && me.Moved:
		return fmt.Sprintf("Warning: Nearhost moved its local CA to %s but could not delete %s (%v). "+
			"That directory still holds a copy of the CA's private key; delete it.", me.New, me.Old, me.Err)
	case errors.As(err, &me):
		return fmt.Sprintf("Warning: Nearhost could not move its local CA from %s to %s (%v), so it keeps using %s.",
			me.Old, me.New, me.Err, me.Old)
	default:
		return fmt.Sprintf("Warning: Nearhost could not move its local CA to its data directory: %v.", err)
	}
}

func newTrustCmd(b trustBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "trust",
		Short: "Install the Nearhost Local CA into " + trustStore,
		Long:  "Install the Nearhost Local CA into " + trustStore + ".\n\n" + elevationNote,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			migrateCA(cmd.ErrOrStderr())
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
			if err := b.sudoProblem("trust"); err != nil {
				return err
			}
			fmt.Fprintln(out, trustInstalling)
			if err := b.install(cert); err != nil {
				if errors.Is(err, sudo.ErrNotFound) {
					return noSudo(err, "trust") // no prompt was shown, and another try won't find sudo
				}
				if trustRefused(err) {
					return errors.New("Nearhost Local CA was not added: the Windows security warning was declined. Run `nearhost trust` again and choose Yes.")
				}
				return fmt.Errorf("%w ("+trustDeclined+", run `nearhost trust` again and approve it)", err)
			}
			fmt.Fprintln(out, "Done. Nearhost Local CA is in "+trustStore+".")
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
		fmt.Fprintln(out, "Nearhost Local CA is already in "+trustStore+". Installing it into the "+nssStores+".")
		if err := b.installNSS(cert); err != nil {
			return fmt.Errorf("%w (the system store already trusts the CA; fix the NSS stores and run `nearhost trust` again)", err)
		}
		fmt.Fprintln(out, "Done. The "+nssStores+" now trust the Nearhost Local CA.")
		return nil
	}
	fmt.Fprintln(out, "Nearhost Local CA is already in "+trustStore+".")
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
	case proxy.NSSNotNeeded:
		return st.Detail + ", so they trust it too."
	default:
		return "The " + nssStores + " were not configured: " + st.Detail + "."
	}
}

// untrustNotInStore is what untrust says when the system store lacks the root.
const untrustNotInStore = "Nearhost Local CA was not in the trust store."

// nssLeftLine words what untrust could not clean from the NSS stores, "" when
// they are clean or there are none. It is the counterpart of nssLine: a
// browser store that still trusts the root, or one that could not be checked
// because certutil is missing, is a CA the user believes is gone.
func nssLeftLine(st proxy.NSSStatus) string {
	var names []string
	for _, s := range st.Stores {
		names = append(names, printable(s))
	}
	list := strings.Join(names, ", ")
	switch {
	case st.State == proxy.NSSCovered:
		if list == "" {
			list = "an NSS store"
		}
		return "Warning: the " + nssStores + " still trust the Nearhost Local CA (" + list +
			"). Nearhost could not remove it there; delete \"Nearhost\" from that browser's certificates by hand."
	case st.State == proxy.NSSUnavailable && st.NoCertutil && len(st.Stores) > 0:
		return "Warning: Nearhost could not clean the " + nssStores + " (" + list + "), which may still trust the Nearhost Local CA: " +
			st.Detail + ". Install it and run `nearhost untrust` again."
	}
	return ""
}

func newUntrustCmd(b trustBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "untrust",
		Short: "Remove the Nearhost Local CA from " + trustStore,
		Long:  "Remove the Nearhost Local CA from " + trustStore + ".\n\n" + elevationNote,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			migrateCA(cmd.ErrOrStderr())
			dir, err := config.CaddyDir()
			if err != nil {
				return err
			}
			cert, err := b.rootCert(dir)
			if err != nil {
				return fmt.Errorf("no local CA found: %w", err)
			}
			out := cmd.OutOrStdout()
			inStore := b.isTrusted == nil || b.isTrusted(cert)
			if inStore {
				// Check sudo only when there is something to remove: the
				// not-trusted path asks for no elevation at all.
				if err := b.sudoProblem("untrust"); err != nil {
					return err
				}
				if err := b.uninstall(cert); err != nil {
					switch {
					case errors.Is(err, truststore.ErrNotFound):
						inStore = false // trusted a moment ago, gone now
					case errors.Is(err, sudo.ErrNotFound):
						return noSudo(err, "untrust")
					case trustRefused(err):
						return errors.New("Nearhost Local CA was not removed: the Windows security warning was declined. Run `nearhost untrust` again and choose Yes.")
					default:
						return fmt.Errorf("%w ("+trustDeclined+", run `nearhost untrust` again and approve it)", err)
					}
				}
			}
			if inStore {
				fmt.Fprintln(out, "Nearhost Local CA removed from the trust store.")
			} else {
				fmt.Fprintln(out, untrustNotInStore)
				// The root can sit in a browser store without being in the
				// system store, and that needs no password.
				if b.uninstallNSS != nil {
					if err := b.uninstallNSS(cert); err != nil {
						return fmt.Errorf("%w (the system store does not hold the CA; fix the NSS stores and run `nearhost untrust` again)", err)
					}
				}
			}
			// The library removes from the NSS stores when it can and skips
			// them silently otherwise, so check what actually happened. The
			// exit status stays 0 as in `trust`, which also only reports NSS
			// stores it could not configure; the line comes last.
			if b.nss != nil {
				if line := nssLeftLine(b.nss(cert)); line != "" {
					fmt.Fprintln(out, line)
				}
			}
			return nil
		},
	}
}
