package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard" // register Caddy's standard modules
)

// caddyMu serialises every call into Caddy's process-global Load/Stop, and
// guards owner: the Proxy whose configuration Caddy is currently running.
var (
	caddyMu sync.Mutex
	owner   *Proxy
)

// Proxy is the embedded Caddy instance. Caddy is process-global, so only one
// Proxy can own it at a time: Apply on a second Proxy takes ownership, after
// which the first one is no longer Running and its Stop does nothing.
type Proxy struct {
	opts Options
}

// New prepares a proxy. Nothing listens until Apply is called.
func New(opts Options) *Proxy {
	return &Proxy{opts: opts}
}

// Apply loads a configuration serving exactly the given sites. Existing
// connections survive; it is safe to call repeatedly and concurrently.
func (p *Proxy) Apply(sites []Site) error {
	cfg, err := BuildConfig(sites, p.opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.opts.StorageRoot, 0o700); err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if err := caddy.Load(cfg, false); err != nil {
		return fmt.Errorf("load proxy config: %w", err)
	}
	owner = p
	return nil
}

// Stop shuts Caddy down and releases its listening ports if this Proxy owns
// it. Already-upgraded connections (WebSockets) are not force-closed; they stay
// open until either end closes or the stream close delay elapses. Calling
// it again, or on a Proxy that never applied or lost ownership, is a no-op.
func (p *Proxy) Stop() error {
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if owner != p {
		return nil
	}
	owner = nil
	return caddy.Stop()
}

// Running reports whether this Proxy's configuration is the one running.
func (p *Proxy) Running() bool {
	caddyMu.Lock()
	defer caddyMu.Unlock()
	return owner == p
}

// RootCertPath is where Caddy stores the local CA's root certificate.
func RootCertPath(storageRoot string) string {
	return filepath.Join(storageRoot, "pki", "authorities", "local", "root.crt")
}

// RootCert loads the local CA root certificate.
func RootCert(storageRoot string) (*x509.Certificate, error) {
	data, err := os.ReadFile(RootCertPath(storageRoot))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("root.crt is not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}
