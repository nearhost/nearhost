//go:build unix

package proxy

import (
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/smallstep/truststore"

	"github.com/nearhost/nearhost/internal/sudo"
)

func TestInstallAndUninstallRunTheTrustedSudo(t *testing.T) {
	trusted := fakeTrustSudo(t)
	before := os.Getenv("PATH")
	var seen []string
	look := func(*x509.Certificate, ...truststore.Option) error {
		path, err := exec.LookPath("sudo")
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, path)
		return nil
	}
	origInstall, origUninstall, origRemove := trustInstall, trustUninstall, removeSystemCopy
	trustInstall, trustUninstall = look, look
	removeSystemCopy = func(*x509.Certificate) error { return nil }
	t.Cleanup(func() { trustInstall, trustUninstall, removeSystemCopy = origInstall, origUninstall, origRemove })

	if err := Install(&x509.Certificate{}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(&x509.Certificate{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != trusted || seen[1] != trusted {
		t.Fatalf("sudo resolved to %q, want %q both times", seen, trusted)
	}
	if got := os.Getenv("PATH"); got != before {
		t.Fatalf("PATH after = %q, want it restored to %q", got, before)
	}
}

func TestInstallRefusesWithoutTrustedSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root needs no sudo")
	}
	origCandidates := sudo.Candidates
	sudo.Candidates = []string{filepath.Join(t.TempDir(), "missing")}
	t.Cleanup(func() { sudo.Candidates = origCandidates })
	called := false
	origInstall := trustInstall
	trustInstall = func(*x509.Certificate, ...truststore.Option) error { called = true; return nil }
	t.Cleanup(func() { trustInstall = origInstall })

	if err := Install(&x509.Certificate{}); err == nil {
		t.Fatal("Install with no root-owned sudo succeeded, want an error")
	}
	if called {
		t.Fatal("the trust store was touched without a trusted sudo")
	}
}

func TestInstallWithEmptyPathAddsNoCurrentDirectory(t *testing.T) {
	trusted := fakeTrustSudo(t)
	t.Setenv("PATH", "")
	var during string
	origInstall := trustInstall
	trustInstall = func(*x509.Certificate, ...truststore.Option) error { during = os.Getenv("PATH"); return nil }
	t.Cleanup(func() { trustInstall = origInstall })

	if err := Install(&x509.Certificate{}); err != nil {
		t.Fatal(err)
	}
	if during != filepath.Dir(trusted) {
		t.Fatalf("PATH during install = %q, want %q alone", during, filepath.Dir(trusted))
	}
}
