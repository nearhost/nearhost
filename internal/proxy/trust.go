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

	"github.com/nearhost/nearhost/internal/sudo"
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

// Uninstall removes the root from the trust stores Install touched.
func Uninstall(cert *x509.Certificate) error {
	if err := withTrustedSudo(func() error { return trustUninstall(cert, truststore.WithFirefox()) }); err != nil {
		return fmt.Errorf("uninstall CA: %w", err)
	}
	return nil
}

// withTrustedSudo runs fn with the directory of sudo.Binary first on PATH.
// The trust store library runs plain "sudo", which asks for the password on
// the terminal, so a fake sudo earlier on PATH would be handed it. Without a
// root-owned sudo it refuses, unless Nearhost runs as root. PATH is
// process-wide, which is fine for the one-shot trust and untrust commands,
// the only callers.
func withTrustedSudo(fn func() error) error {
	path, err := sudo.Binary()
	if err != nil {
		if os.Geteuid() == 0 {
			return fn()
		}
		return err
	}
	orig, had := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+orig); err != nil {
		return err
	}
	defer func() {
		if had {
			os.Setenv("PATH", orig)
		} else {
			os.Unsetenv("PATH")
		}
	}()
	return fn()
}

// IsTrusted reports whether the system trust store accepts cert as a root.
func IsTrusted(cert *x509.Certificate) bool {
	_, err := cert.Verify(x509.VerifyOptions{})
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
)

// NSSStatus is a NSSState with a human-readable reason when unavailable.
type NSSStatus struct {
	State  NSSState
	Detail string
}

// CheckNSS reports whether the NSS stores already have cert. It only
// reads: it never installs anything.
func CheckNSS(cert *x509.Certificate) NSSStatus {
	t, err := truststore.NewNSSTrust()
	if err != nil {
		detail := "NSS support is not available on this platform"
		if truststore.CertutilInstallHelp != "" {
			detail = `"certutil" not found (install it with "` + truststore.CertutilInstallHelp + `")`
		}
		return NSSStatus{State: NSSUnavailable, Detail: detail}
	}
	if err := t.PreCheck(); err != nil {
		return NSSStatus{State: NSSUnavailable, Detail: "no NSS database found"}
	}
	if t.Exists(cert) {
		return NSSStatus{State: NSSCovered}
	}
	return NSSStatus{State: NSSMissing}
}

// InstallNSS adds the root to the NSS stores only, leaving the system
// store alone, so it never prompts for a password.
func InstallNSS(cert *x509.Certificate) error {
	if err := truststore.Install(cert, truststore.WithFirefox(), truststore.WithNoSystem()); err != nil {
		return fmt.Errorf("install CA in NSS stores: %w", err)
	}
	return nil
}
