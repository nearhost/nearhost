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

// caddyMu serialises every call into Caddy's process-global Load/Stop, across
// all Proxy values.
var caddyMu sync.Mutex

// Proxy is the embedded Caddy instance. Only one may run per process.
type Proxy struct {
	opts    Options
	mu      sync.Mutex
	running bool
}

// New prepares a proxy. Nothing listens until Apply is called.
func New(opts Options) *Proxy {
	return &Proxy{opts: opts}
}

// Apply loads a configuration serving exactly the given sites. Existing
// connections survive; it is safe to call repeatedly and concurrently.
func (p *Proxy) Apply(sites []Site) error {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	p.running = true
	return nil
}

// Stop shuts Caddy down and releases its ports. Calling it again is a no-op.
func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return nil
	}
	p.running = false
	caddyMu.Lock()
	defer caddyMu.Unlock()
	return caddy.Stop()
}

// Running reports whether a configuration has been loaded.
func (p *Proxy) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
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
