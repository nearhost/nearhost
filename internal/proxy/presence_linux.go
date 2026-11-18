package proxy

import (
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/smallstep/truststore"
)

// anchorPath is the file the trust store library writes the root to on Linux,
// or "" when it found no anchor directory. It mirrors the library's own
// systemTrustFilename, which is not exported: the directory format it picked at
// start-up, filled with the certificate's common name and serial, spaces
// replaced by underscores.
func anchorPath(cert *x509.Certificate) string {
	if truststore.SystemTrustFilename == "" {
		return ""
	}
	name := "Truststore Development CA " + cert.SerialNumber.String()
	if cn := cert.Subject.CommonName; cn != "" {
		name = cn + " " + cert.SerialNumber.String()
	}
	return fmt.Sprintf(truststore.SystemTrustFilename, strings.ReplaceAll(name, " ", "_"))
}

// SystemHolds reports whether the system store holds the root, judged by the
// anchor file rather than by whether this process trusts it: SSL_CERT_FILE, or
// a bundle not yet regenerated, can make IsTrusted false while the anchor
// stays and trusts the root again at the next update.
func SystemHolds(cert *x509.Certificate) bool {
	p := anchorPath(cert)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// StillTrusted reports whether the system store still holds the root after an
// uninstall. Go caches the system pool for the life of the process, so on
// Linux IsTrusted would answer from before the uninstall; the anchor file is
// the thing to look at.
func StillTrusted(cert *x509.Certificate) bool { return SystemHolds(cert) }
