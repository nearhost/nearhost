package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallstep/truststore"
)

// TestAnchorPathMatchesTheLibraryNaming pins a copy of the library's naming
// (truststore v0.13.0: common name and serial, spaces as underscores), which
// the library does not export. Re-check it when the library version changes.
func TestAnchorPathMatchesTheLibraryNaming(t *testing.T) {
	orig := truststore.SystemTrustFilename
	truststore.SystemTrustFilename = "/anchors/%s.crt"
	t.Cleanup(func() { truststore.SystemTrustFilename = orig })
	cert := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Nearhost Local CA"}}
	if got, want := anchorPath(cert), "/anchors/Nearhost_Local_CA_42.crt"; got != want {
		t.Errorf("anchorPath = %q, want %q", got, want)
	}
	if got, want := anchorPath(&x509.Certificate{SerialNumber: big.NewInt(7)}), "/anchors/Truststore_Development_CA_7.crt"; got != want {
		t.Errorf("anchorPath without a name = %q, want %q", got, want)
	}
}

func TestSystemHoldsFollowsTheAnchorFile(t *testing.T) {
	dir := t.TempDir()
	orig := truststore.SystemTrustFilename
	truststore.SystemTrustFilename = filepath.Join(dir, "%s.crt")
	t.Cleanup(func() { truststore.SystemTrustFilename = orig })
	cert := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Nearhost Local CA"}}
	if SystemHolds(cert) || StillTrusted(cert) {
		t.Fatal("no anchor file: want false")
	}
	if err := os.WriteFile(anchorPath(cert), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !SystemHolds(cert) || !StillTrusted(cert) {
		t.Fatal("anchor file present: want true")
	}
	truststore.SystemTrustFilename = ""
	if SystemHolds(cert) {
		t.Fatal("no anchor directory: want false")
	}
}

// TestUninstallNSSKeepsTheAnchorWithTheRealLibrary runs the library's own
// uninstall: dropping WithNoSystem would make it remove the anchor file.
func TestUninstallNSSKeepsTheAnchorWithTheRealLibrary(t *testing.T) {
	dir := t.TempDir()
	orig, origCmd := truststore.SystemTrustFilename, truststore.SystemTrustCommand
	truststore.SystemTrustFilename = filepath.Join(dir, "%s.crt")
	truststore.SystemTrustCommand = []string{"update-ca-certificates"}
	t.Cleanup(func() { truststore.SystemTrustFilename, truststore.SystemTrustCommand = orig, origCmd })
	t.Setenv("PATH", t.TempDir()) // no sudo, certutil or rm can run

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "Nearhost Local CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	anchor := anchorPath(cert)
	if err := os.WriteFile(anchor, der, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallNSS(cert); err != nil {
		t.Fatalf("UninstallNSS = %v", err)
	}
	if _, err := os.Stat(anchor); err != nil {
		t.Fatalf("anchor file: %v, want it untouched", err)
	}
}
