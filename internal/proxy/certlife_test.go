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
// shortens a leaf to what is left of its intermediate. Caddy checks for
// renewals every minute, so a computer that wakes is served a due
// certificate for a minute at most.
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
	if got := dig(t, cfg, "apps", "tls", "automation", "renew_interval"); got != "1m" {
		t.Errorf("renew_interval = %v, want 1m", got)
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
	root, err := EnsureCA(storage, nil)
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

// Caddy's own check replaces a certificate that comes due, with no restart:
// a connection opened before the renewal still carries requests after it.
func TestCaddyRenewsADueCertificateWithoutARestart(t *testing.T) {
	prevLife, prevEvery := leafLifetime, renewInterval
	// Caddy renews a certificate with less than 5 checks' worth of life
	// left: this one after 3 s.
	leafLifetime, renewInterval = 8*time.Second, time.Second
	t.Cleanup(func() { leafLifetime, renewInterval = prevLife, prevEvery })

	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}})
	h.waitCerts(t, "a.test")
	first := h.leaf(t, "a.test")
	if got := first.NotAfter.Sub(first.NotBefore); got != leafLifetime {
		t.Fatalf("leaf lasts %v, want %v", got, leafLifetime)
	}

	// One kept-alive connection, opened before the renewal.
	kept := h.client("a.test", true)
	resp, body := h.getEventually(kept, "a.test", "/hello")
	if resp.StatusCode != 200 || body != "hello from a" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	opened := resp.TLS.PeerCertificates[0]

	var next *x509.Certificate
	waitUntil(t, 8*time.Second, func() bool {
		state, err := h.handshake(t, "a.test")
		if err != nil || len(state.PeerCertificates) == 0 {
			return false
		}
		next = state.PeerCertificates[0]
		return next.SerialNumber.Cmp(first.SerialNumber) != 0
	}, "Caddy to renew the certificate for a.test")
	if !next.NotAfter.After(first.NotAfter) {
		t.Errorf("the new certificate ends %v, not after the old one's %v", next.NotAfter, first.NotAfter)
	}

	// The same connection: its certificate is the one it opened with.
	resp, err := kept.Get(h.url("a.test", "/hello"))
	if err != nil {
		t.Fatalf("the connection opened before the renewal: %v", err)
	}
	resp.Body.Close()
	if !resp.TLS.PeerCertificates[0].Equal(opened) {
		t.Error("the request went over a new connection: the old one was dropped")
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
