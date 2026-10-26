package proxy

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/smallstep/truststore"
)

// securityCall is one run of the security tool a test saw.
type securityCall struct {
	asRoot bool
	args   []string
}

// fakeSecurity replaces the security tool with one that answers listing
// for find-certificate and fails delete-certificate with deleteErr, and
// returns the calls it saw.
func fakeSecurity(t *testing.T, listing string, deleteErr error) *[]securityCall {
	t.Helper()
	var calls []securityCall
	orig := security
	security = func(asRoot bool, args ...string) ([]byte, error) {
		calls = append(calls, securityCall{asRoot, slices.Clone(args)})
		if args[0] == "delete-certificate" {
			if deleteErr != nil {
				return []byte("Unable to delete certificate\n"), deleteErr
			}
			return nil, nil
		}
		return []byte(listing), nil
	}
	t.Cleanup(func() { security = orig })
	return &calls
}

func keychainHash(cert *x509.Certificate) string {
	sum := sha1.Sum(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestDeleteFromKeychainDeletesTheRootAsRoot(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("root")}
	hash := keychainHash(cert)
	calls := fakeSecurity(t, "SHA-1 hash: 0000\nSHA-1 hash: "+hash+"\n", nil)

	if err := deleteFromKeychain(cert, "/k"); err != nil {
		t.Fatal(err)
	}
	want := []securityCall{
		{false, []string{"find-certificate", "-a", "-Z", "/k"}},
		{true, []string{"delete-certificate", "-Z", hash, "/k"}},
	}
	if !slices.EqualFunc(*calls, want, func(a, b securityCall) bool {
		return a.asRoot == b.asRoot && slices.Equal(a.args, b.args)
	}) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestDeleteFromKeychainLeavesOtherCertificates(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("root")}
	// A longer hash that starts with this one's is another certificate.
	calls := fakeSecurity(t, "SHA-1 hash: "+keychainHash(cert)+"00\n", nil)

	if err := deleteFromKeychain(cert, "/k"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v, want only the listing", *calls)
	}
}

func TestDeleteFromKeychainReportsAFailedDelete(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("root")}
	fakeSecurity(t, "SHA-1 hash: "+keychainHash(cert)+"\n", errors.New("exit status 1"))

	err := deleteFromKeychain(cert, "/k")
	if err == nil || !strings.Contains(err.Error(), "Unable to delete certificate") {
		t.Fatalf("err = %v, want the tool's output", err)
	}
}

func TestUninstallRemovesTheSystemCopyAfterTheTrust(t *testing.T) {
	fakeTrustSudo(t)
	var order []string
	origUninstall, origRemove := trustUninstall, removeSystemCopy
	trustUninstall = func(*x509.Certificate, ...truststore.Option) error { order = append(order, "trust"); return nil }
	removeSystemCopy = func(*x509.Certificate) error { order = append(order, "copy"); return nil }
	t.Cleanup(func() { trustUninstall, removeSystemCopy = origUninstall, origRemove })

	if err := Uninstall(&x509.Certificate{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"trust", "copy"}) {
		t.Fatalf("order = %v, want trust then copy", order)
	}
}
