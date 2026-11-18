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

	"github.com/smallstep/truststore"
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
	// nssUninstalls counts uninstallNSS calls; sudoChecks counts findSudo calls.
	nssUninstalls int
	nssUninstErr  error
	sudoChecks    int
	// present is what the fake system store holds regardless of trusted;
	// stillTrusted is the answer after the uninstall.
	present      bool
	stillTrusted bool
	// foreign makes the fake system store hold no anchor of Nearhost's even
	// though trusted is true: trust comes from somewhere else.
	foreign bool
	// running is whether another Nearhost answers; interactive is whether a
	// question can be asked; removeErr fails the CA's removal, which
	// otherwise goes to proxy.RemoveCA.
	running, interactive bool
	removeErr            error
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
		uninstallNSS: func(*x509.Certificate) error {
			f.nssUninstalls++
			return f.nssUninstErr
		},
		systemHolds:  func(*x509.Certificate) bool { return (f.present || f.trusted) && !f.foreign },
		stillTrusted: func(*x509.Certificate) bool { return f.stillTrusted },
		anchorHelp:   func() (string, string) { return "/usr/local/share/ca-certificates", "update-ca-certificates" },
		findSudo:     func() error { f.sudoChecks++; return nil },
		running:      func() bool { return f.running },
		interactive:  func() bool { return f.interactive },
		removeCA: func(dir string) error {
			if f.removeErr != nil {
				return f.removeErr
			}
			return proxy.RemoveCA(dir)
		},
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
	if !strings.Contains(out, trustStore) || !strings.Contains(out, "NSS") || !strings.Contains(out, "already trust") || strings.Contains(out, "Firefox already") {
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
	f.trusted = true
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
	f.trusted = true
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
		f.trusted = name == "untrust"
		cmd := newTrustCmd(f.backend())
		if name == "untrust" {
			cmd = newUntrustCmd(f.backend())
		}
		_, err := run(t, cmd, name)
		if !errors.Is(err, sudo.ErrNotFound) || strings.Contains(err.Error(), "again") || !strings.Contains(err.Error(), "as root") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// Without sudo the commands stop before promising a password prompt, and
// say what to do.
func TestTrustCommandsCheckForSudoFirst(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"trust", "untrust"} {
		f := newFake()
		f.trusted = name == "untrust" // untrust only asks for sudo when there is something to remove
		b := f.backend()
		b.findSudo = func() error { return sudo.ErrNotFound }
		cmd := newTrustCmd(b)
		if name == "untrust" {
			cmd = newUntrustCmd(b)
		}
		out, err := run(t, cmd, name)
		want := "install sudo in one of those, or run `nearhost " + name + "` as root"
		if !errors.Is(err, sudo.ErrNotFound) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if strings.Contains(out, "password") || f.installs != 0 || f.uninstall != 0 {
			t.Errorf("%s: out %q, installs %d, uninstalls %d", name, out, f.installs, f.uninstall)
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

func TestUntrustNotTrustedSaysSoAsksNoElevationAndCleansNSS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.nss = proxy.NSSStatus{State: proxy.NSSMissing}
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil {
		t.Fatal(err)
	}
	if f.uninstall != 0 || f.sudoChecks != 0 || f.nssUninstalls != 1 {
		t.Fatalf("uninstalls=%d sudoChecks=%d nssUninstalls=%d", f.uninstall, f.sudoChecks, f.nssUninstalls)
	}
	if !strings.Contains(out, "Nearhost Local CA was not in the trust store.") || strings.Contains(out, "removed") || strings.Contains(out, "Warning") {
		t.Fatalf("output %q", out)
	}
}

func TestUntrustMapsNotFoundAfterTrustedToNotInStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.uninstErr = fmt.Errorf("uninstall CA: %w", truststore.ErrNotFound)
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil || f.uninstall != 1 || f.nssUninstalls != 1 {
		t.Fatalf("err=%v uninstalls=%d nssUninstalls=%d", err, f.uninstall, f.nssUninstalls)
	}
	if !strings.Contains(out, "Nearhost Local CA was not in the trust store.") {
		t.Fatalf("output %q", out)
	}
}

func TestUntrustTrustedRemovesAndChecksNSS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSMissing}
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil || f.uninstall != 1 || f.nssUninstalls != 0 || f.nssChecks != 1 {
		t.Fatalf("err=%v uninstalls=%d nssUninstalls=%d nssChecks=%d", err, f.uninstall, f.nssUninstalls, f.nssChecks)
	}
	if !strings.Contains(out, "removed") || strings.Contains(out, "Warning") {
		t.Fatalf("output %q", out)
	}
}

func TestUntrustNSSLeftBehindIsTheLastLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSCovered, Stores: []string{"/home/u/.mozilla/firefox/ab\x1b[31mc.default"}}
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || !strings.Contains(err.Error(), "still trust") || !strings.Contains(err.Error(), "firefox/ab") || strings.Contains(err.Error()+out, "\x1b") {
		t.Fatalf("err=%v output %q", err, out)
	}
}

func TestUntrustCertutilMissingNamesStoresAndWhatToInstall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, NoCertutil: true,
		Detail: `"certutil" not found (install it with "apt install libnss3-tools")`, Stores: []string{"/home/u/.pki/nssdb"}}
	_, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || !strings.Contains(err.Error(), "/home/u/.pki/nssdb") || !strings.Contains(err.Error(), "libnss3-tools") || !strings.Contains(err.Error(), "may still trust") {
		t.Fatalf("err = %v", err)
	}
}

func TestUntrustNoNSSDatabaseSaysNothingExtra(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, Detail: "no NSS database found"}
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil || strings.Contains(out, "Warning") {
		t.Fatalf("err=%v output %q", err, out)
	}
}

func TestUntrustNSSCleanFailureFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.nssUninstErr = errors.New("certutil -D failed")
	_, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || strings.Contains(err.Error(), "certutil -D failed") || !strings.Contains(err.Error(), "quit Firefox") {
		t.Fatalf("err = %v, want the cause without the raw error", err)
	}
}

func TestUntrustAnchorPresentButNotTrustedStillUninstalls(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.present = true // for example SSL_CERT_FILE hides the anchor from this process
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil || f.uninstall != 1 || f.sudoChecks != 1 || strings.Contains(out, "was not in") {
		t.Fatalf("err=%v uninstalls=%d sudoChecks=%d out=%q", err, f.uninstall, f.sudoChecks, out)
	}
}

func TestUntrustStillTrustedAfterUninstallFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for name, uninstErr := range map[string]error{"removed": nil, "not found": truststore.ErrNotFound} {
		f := newFake()
		f.trusted = true
		f.stillTrusted = true
		f.uninstErr = uninstErr
		out, err := run(t, newUntrustCmd(f.backend()), "untrust")
		if err == nil || !strings.Contains(err.Error(), "still") || strings.Contains(out, "was not in") {
			t.Errorf("%s: err=%v out=%q", name, err, out)
		}
	}
}

func TestStillTrustedErrorNamesTheStore(t *testing.T) {
	for goos, want := range map[string]string{"windows": "certlm.msc", "darwin": "Keychain Access", "linux": "update"} {
		if err := stillTrustedError(goos); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", goos, err, want)
		}
	}
}

func TestUntrustNotTrustedWithNSSStillTrustingFailsLast(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.nss = proxy.NSSStatus{State: proxy.NSSCovered, Stores: []string{"/home/u/.mozilla/firefox/a.default"}}
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || !strings.Contains(err.Error(), "still trust") || !strings.Contains(err.Error(), "Snap and Flatpak") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out, "was not in the trust store") {
		t.Fatalf("out %q", out)
	}
}

func TestUntrustNotTrustedWithCertutilMissingFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, NoCertutil: true, Detail: "certutil not found", Stores: []string{"/home/u/.pki/nssdb"}}
	_, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || !strings.Contains(err.Error(), "/home/u/.pki/nssdb") {
		t.Fatalf("err=%v", err)
	}
}

func TestUntrustTrustedFromAnotherAnchorFailsWithoutSudo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.trusted = true
	f.foreign = true
	setHostOS(t, "linux")
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || f.uninstall != 0 || f.sudoChecks != 0 || strings.Contains(out, "removed") {
		t.Fatalf("err=%v uninstalls=%d sudoChecks=%d out=%q", err, f.uninstall, f.sudoChecks, out)
	}
	for _, want := range []string{"did not install the file", "/usr/local/share/ca-certificates", "SSL_CERT_FILE", "update-ca-certificates"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
}

func setHostOS(t *testing.T, goos string) {
	t.Helper()
	orig := hostOS
	hostOS = goos
	t.Cleanup(func() { hostOS = orig })
}

// On macOS a root that is trusted but not held by the System keychain still
// gets its trust settings removed once; the re-check then decides.
func TestUntrustMacTrustedButNotHeldAttemptsTheTrustRemoval(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setHostOS(t, "darwin")

	f := newFake()
	f.trusted, f.foreign = true, true
	out, err := run(t, newUntrustCmd(f.backend()), "untrust")
	if err != nil || f.uninstall != 1 || !strings.Contains(out, "removed") {
		t.Fatalf("not trusted afterwards: err=%v uninstalls=%d out=%q", err, f.uninstall, out)
	}

	f = newFake()
	f.trusted, f.foreign, f.stillTrusted = true, true, true
	_, err = run(t, newUntrustCmd(f.backend()), "untrust")
	if err == nil || f.uninstall != 1 || !strings.Contains(err.Error(), "Keychain Access") {
		t.Fatalf("still trusted afterwards: err=%v uninstalls=%d", err, f.uninstall)
	}
}
