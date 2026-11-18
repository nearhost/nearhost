package proxy

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/smallstep/truststore"
)

// TestAnchorPathMatchesTheLibraryNaming pins the file name to the library's
// scheme (common name and serial, spaces as underscores), which the library
// does not export.
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
