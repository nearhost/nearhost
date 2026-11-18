package cli

import (
	"bufio"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/smallstep/truststore"
	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	// systemHolds reports whether the system store holds the root, whatever
	// isTrusted says: a file or keychain entry that is present has to go even
	// when this process does not see it as trusted. nil means unknown.
	systemHolds func(*x509.Certificate) bool
	// anchorHelp names the directory of the anchor file and the command that
	// rebuilds the bundle, for a message about trust Nearhost did not install.
	anchorHelp func() (dir, update string)
	// stillTrusted is asked after the system uninstall; true means the system
	// still trusts the root. nil falls back to isTrusted.
	stillTrusted func(*x509.Certificate) bool
	// uninstallNSS removes the root from the NSS stores alone, without
	// touching the system store. nil skips it.
	uninstallNSS func(*x509.Certificate) error
	// findSudo returns sudo.ErrNotFound when the system store cannot be
	// changed: no root-owned sudo, and Nearhost is not root. nil skips the
	// check.
	findSudo func() error
	// running reports whether a Nearhost is serving on this machine, for
	// `untrust --delete-key`, which must not delete a CA one holds.
	running func() bool
	// interactive reports whether a question can be asked and answered.
	interactive func() bool
	// removeCA deletes the local CA from Caddy's storage directory.
	removeCA func(storageRoot string) error
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

		nss:          proxy.CheckNSS,
		installNSS:   proxy.InstallNSS,
		uninstall:    proxy.Uninstall,
		systemHolds:  proxy.SystemHolds,
		stillTrusted: proxy.StillTrusted,
		anchorHelp:   proxy.AnchorHelp,

		uninstallNSS: proxy.UninstallNSS,
		findSudo:     systemSudo,

		running: nearhostRunning,
		interactive: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
		removeCA: proxy.RemoveCA,
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

// nssCoverage says what the NSS check covers, for the warnings below.
const nssCoverage = " The check covers Firefox profiles in the standard locations and ~/.pki/nssdb; Snap and Flatpak Firefox profiles are neither cleaned nor checked."

// nssLeftLine words what untrust could not clean from the NSS stores, "" when
// they are clean or there are none. It is the counterpart of nssLine: a
// browser store that still trusts the root, or one that could not be checked
// because certutil is missing, is a CA the user believes is gone.
func nssLeftLine(st proxy.NSSStatus) string {
	list := nssStoreList(st)
	switch {
	case st.State == proxy.NSSCovered:
		return "The " + nssStores + " still trust the Nearhost Local CA (" + list +
			"). Nearhost could not remove it there; delete it from that browser's certificates by hand." + nssCoverage
	case st.State == proxy.NSSUnavailable && st.NoCertutil && len(st.Stores) > 0:
		return "Nearhost could not clean the " + nssStores + " (" + list + "), which may still trust the Nearhost Local CA: " +
			st.Detail + ". Install it and run `nearhost untrust` again." + nssCoverage
	}
	return ""
}

// nssStoreList names the NSS stores found, cleaned for printing.
func nssStoreList(st proxy.NSSStatus) string {
	if len(st.Stores) == 0 {
		return "an NSS store"
	}
	names := make([]string, len(st.Stores))
	for i, s := range st.Stores {
		names[i] = printable(s)
	}
	return strings.Join(names, ", ")
}

// hostOS is the operating system the messages are worded for. Tests replace it.
var hostOS = runtime.GOOS

// foreignTrustError is the error for a root the system trusts without a file
// of Nearhost's: there is nothing for untrust to remove.
func foreignTrustError(goos string, help func() (string, string)) error {
	if goos != "linux" {
		return stillTrustedError(goos)
	}
	dir, update := "", ""
	if help != nil {
		dir, update = help()
	}
	if dir == "" {
		dir = "your distribution's trust anchors directory"
	}
	if update == "" {
		update = "your distribution's update command"
	}
	return fmt.Errorf("the system still trusts Nearhost Local CA, and Nearhost did not install the file that makes it so. "+
		"Look for it under %s and the directory named by SSL_CERT_FILE or SSL_CERT_DIR if set, remove it, then run %s.", dir, update)
}

// stillTrustedError says the system still trusts the root after untrust, for
// the operating system, and what to do about it.
func stillTrustedError(goos string) error {
	switch goos {
	case "windows":
		return errors.New("the Local Computer certificate store still trusts Nearhost Local CA. Remove it with certlm.msc (Trusted Root Certification Authorities), as an administrator.")
	case "darwin":
		return errors.New("macOS still trusts Nearhost Local CA from another keychain, probably the login keychain. Open Keychain Access, find Nearhost Local CA and delete it.")
	default:
		return errors.New("the system trust bundle still holds Nearhost Local CA. Delete its file from your distribution's trust anchors directory and run its update command (update-ca-certificates or update-ca-trust).")
	}
}

func newUntrustCmd(b trustBackend) *cobra.Command {
	var deleteKey, yes bool
	cmd := &cobra.Command{
		Use:   "untrust",
		Short: "Remove the Nearhost Local CA from " + trustStore,
		Long:  "Remove the Nearhost Local CA from " + trustStore + ".\n\n" + elevationNote,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deleteKey {
				if yes {
					return errors.New("--yes answers the question --delete-key asks, so it needs --delete-key")
				}
				migrateCA(cmd.ErrOrStderr())
				dir, err := config.CaddyDir()
				if err != nil {
					return err
				}
				cert, err := b.rootCert(dir)
				if err != nil {
					return fmt.Errorf("no local CA found: %w", err)
				}
				return untrustRoot(cmd.OutOrStdout(), b, cert)
			}
			return untrustAndDelete(cmd, b, yes)
		},
	}
	cmd.Flags().BoolVar(&deleteKey, "delete-key", false, "also delete the local CA and its private key; Nearhost makes a new one the next time it runs")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete the local CA without asking")
	return cmd
}

// untrustAndDelete is `nearhost untrust --delete-key`: it untrusts the root,
// then asks, unless yes, and deletes the local CA.
//
// The key goes whatever untrust left behind. A root that something still
// trusts is worse with its key on disk, where anyone who reads it can sign
// certificates that trust accepts; without the key the root signs nothing
// new. The command still fails while trust remains, and says so after the
// deletion.
func untrustAndDelete(cmd *cobra.Command, b trustBackend, yes bool) error {
	// Refuse before anything happens: the two steps are one operation.
	if b.running() {
		return errors.New("a Nearhost is running on this machine, and it holds the local CA in memory: it would go on using the key and could write it back. " +
			"Stop Nearhost first, then run `nearhost untrust --delete-key` again")
	}
	if !yes && !b.interactive() {
		return errors.New("`nearhost untrust --delete-key` asks before it deletes the local CA, and there is no terminal to ask in. " +
			"Run `nearhost untrust --delete-key --yes` to delete it without asking")
	}
	migrateCA(cmd.ErrOrStderr())
	dir, err := config.CaddyDir()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	ca := filepath.Dir(proxy.RootCertPath(dir))
	cert, err := b.rootCert(dir)
	var untrustErr error
	switch {
	case err == nil:
		untrustErr = untrustRoot(out, b, cert)
	case errors.Is(err, fs.ErrNotExist) && !caPresent(ca):
		return fmt.Errorf("no local CA found: %w", err)
	default:
		// A key without a readable root: there is no root to untrust, but the
		// key can still go.
		untrustErr = fmt.Errorf("Nearhost could not read the local CA's root certificate (%w), so it could not check what trusts it", err)
	}
	if !yes {
		fmt.Fprintf(out, "Delete the local CA and its key in %s? Nearhost makes a new one, which you trust again, the next time it runs. [y/N] ", printable(ca))
		if !saidYesDefaultNo(bufio.NewReader(cmd.InOrStdin()), out) {
			fmt.Fprintln(out, "Kept the local CA and its key.")
			return untrustErr
		}
	}
	if err := b.removeCA(dir); err != nil {
		if untrustErr != nil {
			return fmt.Errorf("%w\n%w", untrustErr, err)
		}
		return err
	}
	fmt.Fprintln(out, "Deleted the local CA and its key.")
	if untrustErr != nil {
		return fmt.Errorf("%w\nThe key is gone, so nothing can sign new certificates under that root, but the trust in it remains, "+
			"and `nearhost untrust` can no longer find the CA to remove it: remove Nearhost Local CA by hand", untrustErr)
	}
	return nil
}

// caPresent reports whether the local CA's directory is there.
func caPresent(ca string) bool {
	_, err := os.Lstat(ca)
	return err == nil
}

// untrustRoot removes cert from the system and NSS stores. An error means
// something may still trust it.
func untrustRoot(out io.Writer, b trustBackend, cert *x509.Certificate) error {
	// Presence decides what to remove, with IsTrusted as one input: a
	// process that does not see the root as trusted can still have its
	// anchor on disk.
	trusted := b.isTrusted(cert)
	holds := b.systemHolds == nil || b.systemHolds(cert)
	// Only Linux can tell that trust is not Nearhost's to remove. On
	// macOS a root trusted without a System keychain copy still gets
	// its trust settings removal, then the re-check decides.
	if trusted && !holds && hostOS == "linux" {
		// Trust comes from a file Nearhost did not install, so there
		// is nothing of Nearhost's to remove and no reason for sudo.
		return foreignTrustError(hostOS, b.anchorHelp)
	}
	inStore := trusted || holds
	if inStore {
		// Check sudo only when there is something to remove: the
		// not-trusted path asks for no elevation at all.
		if err := b.sudoProblem("untrust"); err != nil {
			return err
		}
		if err := b.uninstall(cert); err != nil {
			switch {
			case errors.Is(err, truststore.ErrNotFound):
				inStore = false // listed or trusted a moment ago, gone now
			case errors.Is(err, sudo.ErrNotFound):
				return noSudo(err, "untrust")
			case trustRefused(err):
				return errors.New("Nearhost Local CA was not removed: the Windows security warning was declined. Run `nearhost untrust` again and choose Yes.")
			default:
				return fmt.Errorf("%w ("+trustDeclined+", run `nearhost untrust` again and approve it)", err)
			}
		}
		// A root another store still trusts is not removed, and the
		// not-in-store line would be false.
		still := b.stillTrusted
		if still == nil {
			still = b.isTrusted
		}
		if still(cert) {
			return stillTrustedError(hostOS)
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
				where := "an NSS store"
				if b.nss != nil {
					where = nssStoreList(b.nss(cert))
				}
				return fmt.Errorf("Nearhost could not remove the Nearhost Local CA from the NSS stores (%s). A browser may be holding its database open: quit Firefox and run `nearhost untrust` again.", where)
			}
		}
	}
	// The library removes from the NSS stores when it can and skips
	// them silently otherwise, so check what actually happened. Trust
	// left behind is a failure: `nearhost untrust && ...` must not go
	// on while a browser still trusts the root.
	if b.nss != nil {
		if line := nssLeftLine(b.nss(cert)); line != "" {
			return errors.New(line)
		}
	}
	return nil
}
