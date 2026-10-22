package cli

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/sudo"
)

type fakeTrust struct {
	trusted             bool
	ensureErr, instErr  error
	uninstErr, rootErr  error
	cert                *x509.Certificate
	ensured, rootDirs   []string
	installs, uninstall int

	// nss is what the fake reports for the NSS browser stores (Firefox, and on
	// Linux the shared database); nssChecks counts how often it was read.
	nss         proxy.NSSStatus
	nssInstalls int
	nssErr      error
	nssChecks   int
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
		nss: func(*x509.Certificate) proxy.NSSStatus {
			f.nssChecks++
			return f.nss
		},
		installNSS: func(*x509.Certificate) error { f.nssInstalls++; return f.nssErr },
		uninstall:  func(*x509.Certificate) error { f.uninstall++; return f.uninstErr },
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

func TestTrustSkipsInstallWhenSystemAndNSSCovered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSCovered}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 0 || f.nssInstalls != 0 || f.nssChecks != 1 {
		t.Fatalf("err=%v installs=%d nssInstalls=%d nssChecks=%d", err, f.installs, f.nssInstalls, f.nssChecks)
	}
	if !strings.Contains(out, "system") || !strings.Contains(out, "NSS") || !strings.Contains(out, "already trust") || strings.Contains(out, "Firefox already") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustInstallsNSSOnlyWhenSystemAlreadyTrusted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSMissing}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil {
		t.Fatal(err)
	}
	if f.installs != 0 || f.nssInstalls != 1 {
		t.Fatalf("installs=%d nssInstalls=%d, want system untouched and Firefox installed once", f.installs, f.nssInstalls)
	}
	if !strings.Contains(out, "NSS") || !strings.Contains(out, "Chromium") || strings.Contains(out, "password") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustNSSOnlyInstallErrorFailsWithHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSMissing}
	f.nssErr = errors.New("install CA in NSS stores: nope")
	_, err := run(t, newTrustCmd(f.backend()), "trust")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustSystemTrustedNSSUnavailableSucceedsAndSaysSo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, Detail: "certutil not found"}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 0 || f.nssInstalls != 0 {
		t.Fatalf("err=%v installs=%d nssInstalls=%d", err, f.installs, f.nssInstalls)
	}
	if !strings.Contains(out, "certutil not found") || !strings.Contains(out, "not configured") || strings.Contains(out, "Firefox already") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustFullInstallReportsNSSUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, Detail: "no NSS database found"}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 1 || f.nssInstalls != 0 {
		t.Fatalf("err=%v installs=%d nssInstalls=%d", err, f.installs, f.nssInstalls)
	}
	if !strings.Contains(out, "no NSS database found") || strings.Contains(out, "Browsers now trust") || strings.Contains(out, "Firefox trusts") {
		t.Fatalf("output %q", out)
	}
}

func TestTrustFullInstallReportsNSSCovered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.nss = proxy.NSSStatus{State: proxy.NSSCovered}
	out, err := run(t, newTrustCmd(f.backend()), "trust")
	if err != nil || f.installs != 1 || f.nssInstalls != 0 {
		t.Fatalf("err=%v installs=%d nssInstalls=%d", err, f.installs, f.nssInstalls)
	}
	if !strings.Contains(out, "NSS") || !strings.Contains(out, "Chromium") || !strings.Contains(out, "Done") {
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

// Without a root-owned sudo no password prompt appears and another try
// fails the same way, so the error is not told to approve one.
func TestTrustCommandsWithoutSudoGiveNoPromptHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.instErr = fmt.Errorf("install CA: %w", sudo.ErrNotFound)
	f.uninstErr = fmt.Errorf("uninstall CA: %w", sudo.ErrNotFound)
	for _, name := range []string{"trust", "untrust"} {
		cmd := newTrustCmd(f.backend())
		if name == "untrust" {
			cmd = newUntrustCmd(f.backend())
		}
		_, err := run(t, cmd, name)
		if !errors.Is(err, sudo.ErrNotFound) || strings.Contains(err.Error(), "again") {
			t.Errorf("%s: err = %v", name, err)
		}
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

func TestTrustMovesTheCAToTheDataDir(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	old := filepath.Join(cfgHome, "nearhost", "caddy")
	key := filepath.Join("pki", "authorities", "local", "root.key")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(old, key)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, key), []byte("the key"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	f := newFake()
	if _, err := run(t, newTrustCmd(f.backend()), "trust"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(data, "caddy")
	if len(f.ensured) != 1 || f.ensured[0] != want {
		t.Fatalf("CA made in %v, want %s", f.ensured, want)
	}
	if got, err := os.ReadFile(filepath.Join(want, key)); err != nil || string(got) != "the key" {
		t.Fatalf("moved root.key = %q, %v", got, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old CA directory still there: %v", err)
	}
}

func TestMigrateWarningNamesBothDirectories(t *testing.T) {
	if got := migrateWarning(nil); got != "" {
		t.Fatalf("no error: %q", got)
	}
	failed := migrateWarning(&config.MigrateError{Old: "/old/caddy", New: "/new/caddy", Err: errors.New("disk full")})
	if !strings.Contains(failed, "/old/caddy") || !strings.Contains(failed, "/new/caddy") ||
		!strings.Contains(failed, "disk full") || !strings.Contains(failed, "keeps using /old/caddy") {
		t.Fatalf("failed move: %q", failed)
	}
	left := migrateWarning(&config.MigrateError{Old: "/old/caddy", New: "/new/caddy", Moved: true, Err: errors.New("busy")})
	if !strings.Contains(left, "/new/caddy") || !strings.Contains(left, "could not delete /old/caddy") ||
		!strings.Contains(left, "delete it") {
		t.Fatalf("old left behind: %q", left)
	}
}

func TestTrustCarriesOnWhenTheMoveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	old := filepath.Join(cfgHome, "nearhost", "caddy")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	// The data directory's parent can't be written, so the move fails.
	parent := filepath.Dir(data)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700); os.RemoveAll(data) })
	f := newFake()
	root := &cobra.Command{Use: "nearhost", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newTrustCmd(f.backend()))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"trust"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(f.ensured) != 1 || f.ensured[0] != old {
		t.Fatalf("CA made in %v, want the old %s", f.ensured, old)
	}
	if !strings.Contains(errOut.String(), "Warning") || !strings.Contains(errOut.String(), old) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestDoctorWarnsOfAnOldCACopy(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	if err := os.MkdirAll(filepath.Join(data, "caddy"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSCovered}
	for _, c := range checkCA(f.backend()) {
		if c.name == "old CA copy" {
			t.Fatalf("warned with no old directory: %+v", c)
		}
	}
	old := filepath.Join(cfgHome, "nearhost", "caddy")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	checks := checkCA(f.backend())
	last := checks[len(checks)-1]
	if last.name != "old CA copy" || last.level != levelWarn || !strings.Contains(last.detail, old) || last.fix != "delete "+old {
		t.Fatalf("checks %+v", checks)
	}
	if len(f.rootDirs) != 2 || f.rootDirs[1] != filepath.Join(data, "caddy") {
		t.Fatalf("CA read from %v", f.rootDirs)
	}
}
