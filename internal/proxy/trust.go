package proxy

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/caddyserver/caddy/v2"
	"github.com/smallstep/truststore"
)

// EnsureCA creates the local CA if it does not exist and returns its root.
//
// It loads the zero-site config from BuildConfig, which has no HTTP app and so
// binds no ports, lets Caddy create the root, and stops Caddy again. Caddy is
// process-global, so this goes through the same lock as Proxy. If a Proxy is
// running its configuration is left alone: an existing root is simply read,
// and a missing one is an error rather than a reason to replace the proxy.
func EnsureCA(storageRoot string) (*x509.Certificate, error) {
	if cert, err := RootCert(storageRoot); err == nil {
		return cert, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	caddyMu.Lock()
	defer caddyMu.Unlock()

	// Another caller may have created it while we waited for the lock.
	if cert, err := RootCert(storageRoot); err == nil {
		return cert, nil
	}
	if owner != nil {
		return nil, errors.New("create local CA: a proxy is running with a different storage directory")
	}
	if err := os.MkdirAll(storageRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create storage: %w", err)
	}
	cfg, err := BuildConfig(nil, Options{StorageRoot: storageRoot})
	if err != nil {
		return nil, err
	}
	quietCaddy()
	if err := caddy.Load(cfg, false); err != nil {
		return nil, fmt.Errorf("create local CA: %w", err)
	}
	if err := caddy.Stop(); err != nil {
		return nil, err
	}
	return RootCert(storageRoot)
}

// trustInstall and trustUninstall are the trust store library's calls. Tests
// replace them.
var (
	trustInstall   = truststore.Install
	trustUninstall = truststore.Uninstall
)

// Install adds the root to the system trust store (and Firefox where present).
// It prompts for a password through sudo on macOS.
func Install(cert *x509.Certificate) error {
	if err := withTrustedSudo(func() error { return trustInstall(cert, truststore.WithFirefox()) }); err != nil {
		return fmt.Errorf("install CA: %w", err)
	}
	return nil
}

// Uninstall removes the root from the trust stores Install touched, and on
// macOS deletes it from the System keychain too.
func Uninstall(cert *x509.Certificate) error {
	// On macOS a copy with no trust settings makes `remove-trusted-cert` fail
	// every run, so only the copy is deleted then. When it is trusted, a
	// failure still goes on to delete the copy and the caller's re-check
	// decides the outcome.
	var trustErr error
	if !(copyOnlyWhenUntrusted && !trustedNow(cert)) {
		trustErr = withTrustedSudo(func() error { return trustUninstall(cert, truststore.WithFirefox()) })
		if trustErr != nil && !copyOnlyWhenUntrusted {
			return fmt.Errorf("uninstall CA: %w", trustErr)
		}
	}
	if err := removeSystemCopy(cert); err != nil {
		if trustErr != nil {
			return fmt.Errorf("uninstall CA: %w", trustErr)
		}
		return fmt.Errorf("uninstall CA: %w", err)
	}
	return nil
}

// trustedNow is IsTrusted. Tests replace it.
var trustedNow = IsTrusted

// UninstallNSS removes the root from the NSS stores only, leaving the system
// store alone, so it never prompts for a password. A machine without certutil
// or an NSS database has nothing to remove and is not an error.
func UninstallNSS(cert *x509.Certificate) error {
	if err := trustUninstall(cert, truststore.WithFirefox(), truststore.WithNoSystem()); err != nil {
		return fmt.Errorf("uninstall CA from NSS stores: %w", err)
	}
	return nil
}

// IsTrusted reports whether the system trust store accepts cert as a root.
// On Linux it reads the bundle files afresh, so a CA trusted while Nearhost
// runs shows at once; when they cannot be read it asks Go's own pool.
func IsTrusted(cert *x509.Certificate) bool {
	opts := x509.VerifyOptions{}
	if pool, ok := systemRoots(); ok {
		opts.Roots = pool
	}
	_, err := cert.Verify(opts)
	return err == nil
}

// NSSState is what is known about the NSS stores (Firefox profiles, and on Linux the shared ~/.pki/nssdb).
type NSSState int

const (
	// NSSUnavailable means NSS trust cannot be checked or installed
	// here: no certutil, no Firefox profile, or an unsupported platform. This
	// is the zero value so an unset status never claims coverage.
	NSSUnavailable NSSState = iota
	// NSSCovered means every NSS database found has the root.
	NSSCovered
	// NSSMissing means NSS can be configured and lacks the root.
	NSSMissing
	// NSSNotNeeded means no browser here needs NSS set up: on Windows,
	// Chromium-family browsers use the Windows store, and Firefox 120 and
	// later read the roots a user adds there unless that setting is turned
	// off. Nearhost can't set up NSS on Windows either way.
	NSSNotNeeded
)

// NSSStatus is a NSSState with a human-readable reason when unavailable.
type NSSStatus struct {
	State  NSSState
	Detail string
	// NoCertutil is true when NSS stores cannot be read or changed because
	// certutil is missing, as opposed to there being no NSS store at all.
	NoCertutil bool
	// Stores lists the NSS database directories found, so a message can name
	// them. It is empty when there are none or the platform has no NSS.
	Stores []string
}

// nssDatabases lists the NSS database directories on this machine: the Firefox
// profiles and, on Linux, the shared ~/.pki/nssdb, which hold a cert9.db or
// cert8.db. It mirrors where the trust store library looks.
func nssDatabases() []string {
	var dirs []string
	if truststore.NSSProfile != "" {
		dirs, _ = filepath.Glob(truststore.NSSProfile)
	}
	dirs = append(dirs, filepath.Join(os.Getenv("HOME"), ".pki", "nssdb"))
	var found []string
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		for _, db := range []string{"cert9.db", "cert8.db"} {
			if _, err := os.Stat(filepath.Join(d, db)); err == nil {
				found = append(found, d)
				break
			}
		}
	}
	return found
}

// CheckNSS reports whether the NSS stores already have cert. It only
// reads: it never installs anything.
func CheckNSS(cert *x509.Certificate) NSSStatus {
	if !nssNeeded {
		return NSSStatus{State: NSSNotNeeded, Detail: "Chromium-family browsers and Firefox 120 or later use the Windows certificate store"}
	}
	stores := nssDatabases()
	t, err := truststore.NewNSSTrust()
	if err != nil {
		detail := "NSS support is not available on this platform"
		missing := false
		if truststore.CertutilInstallHelp != "" {
			detail = `"certutil" not found (install it with "` + truststore.CertutilInstallHelp + `")`
			missing = true
		}
		return NSSStatus{State: NSSUnavailable, Detail: detail, NoCertutil: missing, Stores: stores}
	}
	if err := t.PreCheck(); err != nil {
		return NSSStatus{State: NSSUnavailable, Detail: "no NSS database found"}
	}
	if t.Exists(cert) {
		return NSSStatus{State: NSSCovered, Stores: stores}
	}
	return NSSStatus{State: NSSMissing, Stores: stores}
}

// InstallNSS adds the root to the NSS stores only, leaving the system
// store alone, so it never prompts for a password.
func InstallNSS(cert *x509.Certificate) error {
	if err := truststore.Install(cert, truststore.WithFirefox(), truststore.WithNoSystem()); err != nil {
		return fmt.Errorf("install CA in NSS stores: %w", err)
	}
	return nil
}
