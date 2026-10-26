package proxy

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nearhost/nearhost/internal/sudo"
)

// systemKeychain is where `security add-trusted-cert -d` puts the root on
// macOS.
const systemKeychain = "/Library/Keychains/System.keychain"

// security runs macOS's security tool, through the trusted sudo when asRoot,
// and returns its combined output. Tests replace it.
var security = func(asRoot bool, args ...string) ([]byte, error) {
	name := "security"
	if asRoot {
		path, err := sudo.Binary()
		switch {
		case err == nil:
			args = append([]string{"--", name}, args...)
			name = path
		case !errors.Is(err, sudo.ErrNotFound) || os.Geteuid() != 0:
			return nil, err
		}
	}
	return exec.Command(name, args...).CombinedOutput()
}

// deleteFromKeychain removes cert from keychain when it is there. The trust
// store library's uninstall runs `security remove-trusted-cert`, which drops
// the trust settings and leaves the certificate in the keychain, so each
// trust and untrust would otherwise leave a copy behind.
func deleteFromKeychain(cert *x509.Certificate, keychain string) error {
	sum := sha1.Sum(cert.Raw)
	hash := strings.ToUpper(hex.EncodeToString(sum[:]))
	out, err := security(false, "find-certificate", "-a", "-Z", keychain)
	if err != nil {
		return fmt.Errorf("list %s: %w", keychain, err)
	}
	if !bytes.Contains(out, []byte("SHA-1 hash: "+hash+"\n")) {
		return nil
	}
	if out, err := security(true, "delete-certificate", "-Z", hash, keychain); err != nil {
		return fmt.Errorf("delete the root from %s: %w: %s", keychain, err, bytes.TrimSpace(out))
	}
	return nil
}
