package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/nearhost/nearhost/internal/testutil"
)

// Every certificate policy issues week-long leaves, and the CA's
// intermediates last 30 days, long enough to sign them: the internal issuer
// shortens a leaf to what is left of its intermediate.
func TestBuildConfigCertificateLifetimes(t *testing.T) {
	raw, err := BuildConfig([]Site{
		{Alias: "a.test", Hosts: []string{"a.test", "*.local.x.io"}, Upstream: "127.0.0.1:1"},
	}, Options{StorageRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	policies := dig(t, cfg, "apps", "tls", "automation", "policies").([]any)
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want the exact-host one and the on-demand one", len(policies))
	}
	for i := range policies {
		if got := dig(t, policies[i], "issuers", 0, "lifetime"); got != "168h" {
			t.Errorf("policy %d issuer lifetime = %v, want 168h", i, got)
		}
	}
	if got := dig(t, cfg, "apps", "pki", "certificate_authorities", "local", "intermediate_lifetime"); got != "720h" {
		t.Errorf("intermediate_lifetime = %v, want 720h", got)
	}

	// The config EnsureCA loads, with no sites, makes the CA's first
	// intermediate: it has the long lifetime too.
	raw, err = BuildConfig(nil, Options{StorageRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg = nil
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := dig(t, cfg, "apps", "pki", "certificate_authorities", "local", "intermediate_lifetime"); got != "720h" {
		t.Errorf("intermediate_lifetime with no sites = %v, want 720h", got)
	}
}

// An install from before week-long leaves has an intermediate with at most
// 7 days left, which would cut every leaf short. Apply replaces it, and the
// site's certificate lasts a week and verifies against the same root.
func TestApplyReplacesAnIntermediateTooShortForAWeek(t *testing.T) {
	storage := testutil.TempDir(t, "nh-caddy-")
	root, err := EnsureCA(storage)
	if err != nil {
		t.Fatal(err)
	}
	old := writeIntermediate(t, storage, 48*time.Hour)

	up := upstream(t, "a")
	h := startProxyOpts(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}}, Options{StorageRoot: storage})
	h.waitCerts(t, "a.test")
	state, err := h.handshake(t, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.VerifiedChains) == 0 {
		t.Fatal("no verified chain")
	}
	chain := state.VerifiedChains[0]
	leaf, inter := chain[0], chain[1]
	if got := leaf.NotAfter.Sub(leaf.NotBefore); got != leafLifetime {
		t.Errorf("leaf lasts %v, want %v", got, leafLifetime)
	}
	if inter.SerialNumber.Cmp(old.SerialNumber) == 0 {
		t.Error("the leaf is signed by the old, short intermediate")
	}
	if left := time.Until(inter.NotAfter); left < intermediateLifetime-time.Hour {
		t.Errorf("the intermediate has %v left, want about %v", left, intermediateLifetime)
	}
	if !chain[len(chain)-1].Equal(root) {
		t.Error("the chain does not end at the root the user trusted")
	}
}

// writeIntermediate replaces the local CA's intermediate in storage with one
// the root signs that lasts life from now, as an older Nearhost left it.
func writeIntermediate(t *testing.T, storage string, life time.Duration) *x509.Certificate {
	t.Helper()
	dir := filepath.Dir(RootCertPath(storage))
	root, err := RootCert(storage)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "root.key"))
	if err != nil {
		t.Fatal(err)
	}
	rootKey, err := certmagic.PEMDecodePrivateKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: CAName + " Old Intermediate"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(life),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, key.Public(), rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyOut, err := certmagic.PEMEncodePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "intermediate.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "intermediate.key"), keyOut, 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// Caddy renews a certificate on its own timer, which a sleeping computer
// stops. Renew, called after a wake, serves a new certificate once the one
// served is due for renewal, and leaves a fresh one alone.
func TestRenewReplacesACertificateDueForRenewal(t *testing.T) {
	prev := leafLifetime
	leafLifetime = 12 * time.Second
	t.Cleanup(func() { leafLifetime = prev })

	up := upstream(t, "a")
	sites := []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}}
	h := startProxy(t, sites)
	h.waitCerts(t, "a.test")
	first := h.leaf(t, "a.test")
	if got := first.NotAfter.Sub(first.NotBefore); got != leafLifetime {
		t.Fatalf("leaf lasts %v, want %v", got, leafLifetime)
	}

	if renewed, err := h.p.Renew(sites); err != nil || renewed {
		t.Fatalf("Renew of a fresh certificate = %v, %v; want false, nil", renewed, err)
	}
	if got := h.leaf(t, "a.test"); got.SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Fatal("a fresh certificate was replaced")
	}

	// Into the last third of its life, where Caddy's timer would renew it.
	time.Sleep(time.Until(first.NotAfter.Add(-leafLifetime / 4)))
	renewed, err := h.p.Renew(sites)
	if err != nil || !renewed {
		t.Fatalf("Renew of a certificate due for renewal = %v, %v; want true, nil", renewed, err)
	}
	if !h.p.Running() {
		t.Fatal("the proxy is not running after Renew")
	}
	var next *x509.Certificate
	waitUntil(t, 10*time.Second, func() bool {
		state, err := h.handshake(t, "a.test")
		if err != nil || len(state.PeerCertificates) == 0 {
			return false
		}
		next = state.PeerCertificates[0]
		return next.SerialNumber.Cmp(first.SerialNumber) != 0
	}, "a new certificate for a.test")
	if !next.NotAfter.After(first.NotAfter) {
		t.Errorf("the new certificate ends %v, not after the old one's %v", next.NotAfter, first.NotAfter)
	}
	resp, body := h.getEventually(h.client("a.test", false), "a.test", "/hello")
	if resp.StatusCode != 200 || body != "hello from a" {
		t.Fatalf("after Renew: status=%d body=%q", resp.StatusCode, body)
	}
}

// Renew on a Proxy that does not own Caddy does nothing.
func TestRenewLeavesAnotherOwnersProxyAlone(t *testing.T) {
	p := New(Options{StorageRoot: t.TempDir()})
	if renewed, err := p.Renew(nil); err != nil || renewed {
		t.Fatalf("Renew = %v, %v; want false, nil", renewed, err)
	}
}

// leaf is the certificate the proxy serves for sni.
func (h *harness) leaf(t *testing.T, sni string) *x509.Certificate {
	t.Helper()
	state, err := h.handshake(t, sni)
	if err != nil {
		t.Fatal(err)
	}
	return state.PeerCertificates[0]
}
