package proxy

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"

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
	if err := caddy.Load(cfg, false); err != nil {
		return nil, fmt.Errorf("create local CA: %w", err)
	}
	if err := caddy.Stop(); err != nil {
		return nil, err
	}
	return RootCert(storageRoot)
}

// Install adds the root to the system trust store (and Firefox where present).
// It prompts for a password through sudo on macOS.
func Install(cert *x509.Certificate) error {
	if err := truststore.Install(cert, truststore.WithFirefox()); err != nil {
		return fmt.Errorf("install CA: %w", err)
	}
	return nil
}

// Uninstall removes the root from the trust stores Install touched.
func Uninstall(cert *x509.Certificate) error {
	if err := truststore.Uninstall(cert, truststore.WithFirefox()); err != nil {
		return fmt.Errorf("uninstall CA: %w", err)
	}
	return nil
}

// IsTrusted reports whether the system trust store accepts cert as a root.
func IsTrusted(cert *x509.Certificate) bool {
	_, err := cert.Verify(x509.VerifyOptions{})
	return err == nil
}
