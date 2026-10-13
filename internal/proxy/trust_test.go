package proxy

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func TestEnsureCACreatesRootUnderStorage(t *testing.T) {
	storage := t.TempDir()
	cert, err := EnsureCA(storage)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("expected a CA certificate")
	}
	if !strings.Contains(cert.Subject.CommonName, CAName) {
		t.Fatalf("subject CN %q should contain %q", cert.Subject.CommonName, CAName)
	}
	onDisk, err := RootCert(storage)
	if err != nil {
		t.Fatalf("root not stored under the given storage: %v", err)
	}
	if onDisk.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatal("returned cert differs from the stored root")
	}
	if IsTrusted(cert) {
		t.Fatal("a fresh CA must not be trusted by the system store")
	}
}

func TestEnsureCATwiceReturnsSameRoot(t *testing.T) {
	storage := t.TempDir()
	first, err := EnsureCA(storage)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCA(storage)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) {
		t.Fatal("second EnsureCA regenerated the root")
	}
}

func TestEnsureCALeavesNoCaddyRunning(t *testing.T) {
	if _, err := EnsureCA(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if owner != nil {
		t.Fatal("EnsureCA left an owning proxy behind")
	}
	if caddy.ActiveContext().Context != nil {
		t.Fatal("EnsureCA left Caddy running")
	}
}

// A zero-site proxy must not listen at all, which is what lets EnsureCA create
// the CA without binding ports 80/443.
func TestZeroSiteProxyOpensNoListeners(t *testing.T) {
	https, http := freePort(t), freePort(t)
	p := New(Options{StorageRoot: t.TempDir(), HTTPSPort: https, HTTPPort: http})
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop() })
	for _, port := range []int{https, http} {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
			t.Fatalf("port %d is listening with zero sites", port)
		}
	}
	if _, err := RootCert(p.opts.StorageRoot); err != nil {
		t.Fatalf("zero-site config did not create the CA root: %v", err)
	}
}

func TestEnsureCAWhileProxyRunningKeepsItServing(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	// The running proxy already created the root in its own storage.
	cert, err := EnsureCA(h.p.opts.StorageRoot)
	if err != nil {
		t.Fatalf("EnsureCA while proxy running: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("expected CA")
	}
	if !h.p.Running() {
		t.Fatal("EnsureCA displaced the running proxy")
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	// Even with a different storage that has no root yet, a running proxy
	// must not be replaced.
	other := t.TempDir()
	_, err = EnsureCA(other)
	if err == nil {
		t.Fatal("expected an error: cannot create a CA elsewhere while a proxy owns Caddy")
	}
	if !h.p.Running() {
		t.Fatal("failed EnsureCA displaced the running proxy")
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
}
