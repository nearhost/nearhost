//go:build !windows

package proxy

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/smallstep/truststore"

	"github.com/nearhost/nearhost/internal/sudo"
)

// TestUninstallNSSLeavesTheSystemStoreAlone checks that the NSS-only
// uninstall never reaches the sudo path or the System keychain copy removal.
func TestUninstallNSSLeavesTheSystemStoreAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root needs no sudo")
	}
	origCandidates := sudo.Candidates
	sudo.Candidates = []string{filepath.Join(t.TempDir(), "missing")} // withTrustedSudo would refuse
	origUninstall, origRemove := trustUninstall, removeSystemCopy
	t.Cleanup(func() { sudo.Candidates, trustUninstall, removeSystemCopy = origCandidates, origUninstall, origRemove })
	var calls, removed int
	trustUninstall = func(*x509.Certificate, ...truststore.Option) error { calls++; return nil }
	removeSystemCopy = func(*x509.Certificate) error { removed++; return nil }
	before := os.Getenv("PATH")
	if err := UninstallNSS(&x509.Certificate{}); err != nil {
		t.Fatalf("UninstallNSS = %v, want it to need no sudo", err)
	}
	if calls != 1 || removed != 0 || os.Getenv("PATH") != before {
		t.Fatalf("calls=%d removed=%d PATH=%q", calls, removed, os.Getenv("PATH"))
	}
}

// TestNSSDatabasesFindsProfilesAndTheSharedDatabase checks that only
// directories holding a certificate database are listed.
func TestNSSDatabasesFindsProfilesAndTheSharedDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orig := truststore.NSSProfile
	truststore.NSSProfile = filepath.Join(home, ".mozilla", "firefox", "*")
	t.Cleanup(func() { truststore.NSSProfile = orig })
	mk := func(dir, db string) string {
		p := filepath.Join(home, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if db != "" {
			if err := os.WriteFile(filepath.Join(p, db), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	a := mk(".mozilla/firefox/a.default", "cert9.db")
	mk(".mozilla/firefox/empty", "")
	shared := mk(".pki/nssdb", "cert9.db")
	if got, want := nssDatabases(), []string{a, shared}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nssDatabases = %v, want %v", got, want)
	}
}
