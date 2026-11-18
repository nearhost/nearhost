package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/standard" // register Caddy's standard modules
	"github.com/caddyserver/certmagic"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/oserr"
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

// SetWebPort sets the port the proxy serves Nearhost's page on, 0 for none:
// a caller that found the port taken drops it, and restores it before trying
// again. It must be called while the proxy is not running, as the options are
// read without a lock.
func (p *Proxy) SetWebPort(port int) {
	p.opts.WebPort = port
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
		return false, fmt.Errorf("create proxy storage: %w", err)
	}
	quietCaddy()
	// A replaced intermediate is read only when Caddy loads the config,
	// which it skips for one it already runs.
	force := freshenIntermediate(p.opts.StorageRoot)
	if err := caddy.Load(cfg, force); err != nil {
		return false, fmt.Errorf("load proxy config: %w", err)
	}
	owner = p
	was := listening
	listening = listens(sites, p.opts)
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

// Renew restarts Caddy with the given sites if this Proxy owns it and a
// certificate it serves for one of their exact hosts, or for localhost, is
// due for renewal, and reports whether it did. Caddy renews certificates on
// a timer, which does not run while the computer sleeps, so after a long
// sleep it serves an expired certificate until the timer next fires. Loading
// the same config again does not help: Caddy skips an unchanged config, and
// a forced reload keeps the certificates it has cached. A restart empties
// that cache, and Caddy checks each certificate as it loads it, renewing
// the ones due. The restart drops open connections and, for a moment,
// refuses new ones. Certificates of patterns are issued on demand and Caddy
// renews those at the handshake, so they are not checked.
func (p *Proxy) Renew(sites []Site) (bool, error) {
	cfg, err := BuildConfig(sites, p.opts)
	if err != nil {
		return false, err
	}
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if owner != p || !certsDue(sites, time.Now()) {
		return false, nil
	}
	if err := caddy.Stop(); err != nil {
		return false, fmt.Errorf("restart proxy: %w", err)
	}
	if listening {
		// Caddy releases the ports in goroutines it does not wait for.
		_ = releasePorts(p.ports(), releaseWait)
	}
	freshenIntermediate(p.opts.StorageRoot)
	if err := caddy.Load(cfg, true); err != nil {
		owner, listening = nil, false
		return false, fmt.Errorf("restart proxy: %w", err)
	}
	listening = listens(sites, p.opts)
	return true, nil
}

// certsDue reports whether a certificate cached for an exact host of sites,
// or for localhost, is in the last third of its life at now, where Caddy's
// timer renews it, or past it.
func certsDue(sites []Site, now time.Time) bool {
	hosts := []string{homeHost}
	for _, s := range sites {
		for _, h := range s.Hosts {
			if !hostname.IsPattern(h) {
				hosts = append(hosts, h)
			}
		}
	}
	for _, h := range hosts {
		for _, c := range caddytls.AllMatchingCertificates(h) {
			if c.Leaf == nil {
				continue
			}
			life := c.Leaf.NotAfter.Sub(c.Leaf.NotBefore)
			window := time.Duration(float64(life) * certmagic.DefaultRenewalWindowRatio)
			if !now.Before(c.Leaf.NotAfter.Add(-window)) {
				return true
			}
		}
	}
	return false
}

// freshenIntermediate removes the local CA's intermediate from storage when
// it has less than a leaf's lifetime left, and reports whether it did, so
// that Caddy makes a new one, with intermediateLifetime, when it next loads
// a config. The internal issuer would otherwise cut every leaf short to what
// is left of it. An install from before week-long leaves has one of at most
// 7 days. New leaves chain to the same root; leaves the old intermediate
// signed carry it in their chain and stay valid until they are renewed.
// Call it with caddyMu held.
func freshenIntermediate(storageRoot string) bool {
	dir := filepath.Dir(RootCertPath(storageRoot))
	cert := filepath.Join(dir, "intermediate.crt")
	data, err := os.ReadFile(cert)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	inter, err := x509.ParseCertificate(block.Bytes)
	if err != nil || time.Until(inter.NotAfter) >= leafLifetime {
		return false
	}
	if err := os.Remove(cert); err != nil {
		return false
	}
	// Caddy writes a new key with the new certificate; removing the old
	// one only keeps the two from being mismatched meanwhile.
	_ = os.Remove(filepath.Join(dir, "intermediate.key"))
	return true
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
	if p.opts.WebPort != 0 {
		return []int{https, http, p.opts.WebPort}
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
		for _, addr := range netbind.ServeAddrs(port) {
			if err := waitAddr(addr, port, deadline); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitAddr waits until addr, an address of port, is free on TCP, or deadline
// has passed.
func waitAddr(addr string, port int, deadline time.Time) error {
	for {
		l, err := netbind.Listen("tcp", addr)
		if err == nil {
			l.Close()
			return nil
		}
		if !oserr.AddrInUse(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("port %d is still in use after the proxy stopped", port)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
