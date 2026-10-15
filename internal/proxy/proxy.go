package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard" // register Caddy's standard modules
)

// caddyMu serialises every call into Caddy's process-global Load/Stop, and
// guards owner, the Proxy whose configuration Caddy is currently running,
// and listening.
var (
	caddyMu sync.Mutex
	owner   *Proxy
	// listening reports whether the running config has listeners, which
	// Stop waits to be released.
	listening bool
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
	_, err := p.apply(sites, false)
	return err
}

// ApplyIfRunning is Apply for a Proxy that only means to change what it is
// already running. If this Proxy does not own Caddy (another one has taken
// over, or this one never applied or has stopped) it loads nothing, takes
// nothing over and reports false. The check and the load are one step, so a
// Proxy cannot take Caddy back from a newer owner this way.
func (p *Proxy) ApplyIfRunning(sites []Site) (bool, error) {
	return p.apply(sites, true)
}

func (p *Proxy) apply(sites []Site, onlyIfOwner bool) (bool, error) {
	cfg, err := BuildConfig(sites, p.opts)
	if err != nil {
		return false, err
	}
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if onlyIfOwner && owner != p {
		return false, nil
	}
	if err := os.MkdirAll(p.opts.StorageRoot, 0o700); err != nil {
		return false, fmt.Errorf("create storage: %w", err)
	}
	quietCaddy()
	if err := caddy.Load(cfg, false); err != nil {
		return false, fmt.Errorf("load proxy config: %w", err)
	}
	owner = p
	was := listening
	listening = len(sites) > 0 // with no sites the config declares no listeners
	if was && !listening {
		// Caddy stops the old listeners as Stop does, in goroutines it
		// does not wait for: wait for the ports, so the proxy never takes
		// for released a port Caddy still holds, and Stop, which waits only
		// while something listens, is not left to race it. The config is
		// applied either way; a port someone else took meanwhile is theirs.
		_ = releasePorts(p.ports(), releaseWait)
	}
	return true, nil
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
	if err := caddy.Stop(); err != nil {
		return err
	}
	if !listening {
		return nil
	}
	listening = false
	// Caddy starts its servers' shutdowns in goroutines and returns once
	// they have started, so a listener can stay open a moment after Stop.
	// Wait for the ports, so a caller can bind them again at once.
	return releasePorts(p.ports(), releaseWait)
}

// releaseWait bounds how long Stop waits for the proxy's ports.
const releaseWait = 5 * time.Second

// ports are the TCP ports the proxy listens on.
func (p *Proxy) ports() []int {
	https, http := p.opts.HTTPSPort, p.opts.HTTPPort
	if https == 0 {
		https = 443
	}
	if http == 0 {
		http = 80
	}
	return []int{https, http}
}

// releasePorts is waitReleased; tests watch it.
var releasePorts = waitReleased

// waitReleased waits until no port is in use on TCP, or timeout has passed.
// A port that cannot be bound for another reason, such as a low port without
// the right to bind it, is not waited for. Only TCP is waited for: Caddy's
// HTTP/3 server keeps its UDP socket until its connections end, which
// nothing here waits for.
func waitReleased(ports []int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, port := range ports {
		for {
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
			if err == nil {
				l.Close()
				break
			}
			if !errors.Is(err, syscall.EADDRINUSE) {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("port %d is still in use after the proxy stopped", port)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nil
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
