package cli

import (
	"bytes"
	"crypto/x509"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/proxy"
)

type fakeTrust struct {
	trusted             bool
	ensureErr, instErr  error
	uninstErr, rootErr  error
	cert                *x509.Certificate
	ensured, rootDirs   []string
	installs, uninstall int

	// firefox is consulted on every call, so a test can model the state
	// before and after an install by flipping it from the hooks below.
	firefox    proxy.FirefoxStatus
	fxInstalls int
	fxErr      error
	fxChecks   int
}

func (f *fakeTrust) backend() trustBackend {
	return trustBackend{
		ensureCA: func(dir string) (*x509.Certificate, error) {
			f.ensured = append(f.ensured, dir)
			return f.cert, f.ensureErr
		},
		rootCert: func(dir string) (*x509.Certificate, error) {
			f.rootDirs = append(f.rootDirs, dir)
			return f.cert, f.rootErr
		},
		isTrusted: func(*x509.Certificate) bool { return f.trusted },
		install:   func(*x509.Certificate) error { f.installs++; return f.instErr },
		firefox: func(*x509.Certificate) proxy.FirefoxStatus {
			f.fxChecks++
			return f.firefox
		},
		installFirefox: func(*x509.Certificate) error { f.fxInstalls++; return f.fxErr },
		uninstall:      func(*x509.Certificate) error { f.uninstall++; return f.uninstErr },
	}
}

func newFake() *fakeTrust { return &fakeTrust{cert: &x509.Certificate{SerialNumber: big.NewInt(1)}} }

func run(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "nearhost", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmd)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestTrustInstallsAndReports(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 || len(f.ensured) != 1 || !strings.HasSuffix(f.ensured[0], "caddy") {
		t.Fatalf("installs=%d ensured=%v", f.installs, f.ensured)
	}
	if !strings.Contains(out, "Done") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustSkipsInstallWhenSystemAndFirefoxCovered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.firefox = proxy.FirefoxStatus{State: proxy.FirefoxCovered}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 0 || f.fxInstalls != 0 {
		t.Fatalf("err=%v installs=%d fxInstalls=%d", err, f.installs, f.fxInstalls)
	}
	if !strings.Contains(out, "system") || !strings.Contains(out, "Firefox") || !strings.Contains(out, "already trusted") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustInstallsFirefoxOnlyWhenSystemAlreadyTrusted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.firefox = proxy.FirefoxStatus{State: proxy.FirefoxMissing}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil {
		t.Fatal(err)
	}
	if f.installs != 0 || f.fxInstalls != 1 {
		t.Fatalf("installs=%d fxInstalls=%d, want system untouched and Firefox installed once", f.installs, f.fxInstalls)
	}
	if !strings.Contains(out, "Firefox") || strings.Contains(out, "password") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustFirefoxOnlyInstallErrorFailsWithHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.firefox = proxy.FirefoxStatus{State: proxy.FirefoxMissing}
	f.fxErr = errors.New("install CA in Firefox: nope")
	_, err := run(t, newTrustCmd(f.backend()), "trust")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustSystemTrustedFirefoxUnavailableSucceedsAndSaysSo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.firefox = proxy.FirefoxStatus{State: proxy.FirefoxUnavailable, Detail: "certutil not found"}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 0 || f.fxInstalls != 0 {
		t.Fatalf("err=%v installs=%d fxInstalls=%d", err, f.installs, f.fxInstalls)
	}
	if !strings.Contains(out, "certutil not found") || !strings.Contains(out, "Firefox") || strings.Contains(out, "Firefox already") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustFullInstallReportsFirefoxUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.firefox = proxy.FirefoxStatus{State: proxy.FirefoxUnavailable, Detail: "no Firefox profile found"}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 1 || f.fxInstalls != 0 {
		t.Fatalf("err=%v installs=%d fxInstalls=%d", err, f.installs, f.fxInstalls)
	}
	if !strings.Contains(out, "no Firefox profile found") || strings.Contains(out, "Browsers now trust") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustFullInstallReportsFirefoxCovered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.firefox = proxy.FirefoxStatus{State: proxy.FirefoxCovered}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 1 || f.fxInstalls != 0 {
		t.Fatalf("err=%v installs=%d fxInstalls=%d", err, f.installs, f.fxInstalls)
	}
	if !strings.Contains(out, "Firefox") || !strings.Contains(out, "Done") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustSurfacesInstallErrorWithHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.instErr = errors.New("install CA: user canceled")
	_, err := run(t, newTrustCmd(f.backend()), "trust")
	if err == nil || !strings.Contains(err.Error(), "user canceled") || !strings.Contains(err.Error(), "run `nearhost trust` again") {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustSurfacesEnsureError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.ensureErr = errors.New("boom")
	if _, err := run(t, newTrustCmd(f.backend()), "trust"); err == nil || f.installs != 0 {
		t.Fatalf("err=%v installs=%d", err, f.installs)
	}
}

func TestUntrustRemoves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil || f.uninstall != 1 || !strings.Contains(out, "removed") {
		t.Fatalf("err=%v uninstalls=%d out=%q", err, f.uninstall, out)
	}
}

func TestUntrustWithoutCAFailsWithoutUninstalling(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.rootErr = errors.New("missing")
	_, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || !strings.Contains(err.Error(), "no local CA found") || f.uninstall != 0 {
		t.Fatalf("err=%v uninstalls=%d", err, f.uninstall)
	}
}

func TestUntrustSurfacesErrorWithHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.uninstErr = errors.New("uninstall CA: refused")
	_, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "run `nearhost untrust` again") {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustCommandsRejectArguments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	if _, err := run(t, newTrustCmd(f.backend()), "trust", "extra"); err == nil {
		t.Error("trust accepted a stray argument")
	}
	if _, err := run(t, newUntrustCmd(f.backend()), "untrust", "extra"); err == nil {
		t.Error("untrust accepted a stray argument")
	}
	if f.installs != 0 || f.uninstall != 0 || len(f.ensured) != 0 {
		t.Error("backend touched despite rejected arguments")
	}
}

func TestRootRegistersTrustCommands(t *testing.T) {
	root := NewRoot()
	for _, name := range []string{"trust", "untrust"} {
		if c, _, err := root.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("%s not registered: %v", name, err)
		}
	}
	// Stray args are rejected before RunE, so this never reaches the system store.
	root.SetArgs([]string{"trust", "extra"})
	if err := root.Execute(); err == nil {
		t.Error("real trust command accepted a stray argument")
	}
}
