package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

// The DNS token comes from its file in the data directory, trimmed, and
// otherwise from $NEARHOST_DNS_TOKEN; with neither there is none, and the
// error says where it looked without holding a token.
func TestDNSToken(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", testutil.TempDir(t, "nh"))
	t.Setenv(dnsTokenEnv, "")
	if token, err := dnsToken(); err == nil || token != "" {
		t.Errorf("dnsToken with none = %q, %v", token, err)
	}
	t.Setenv(dnsTokenEnv, "from-the-environment-0123456789")
	if token, err := dnsToken(); err != nil || token != "from-the-environment-0123456789" {
		t.Errorf("dnsToken from env = %q, %v", token, err)
	}
	path, err := config.DNSTokenPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := dnsToken(); err != nil || token != "from-the-environment-0123456789" {
		t.Errorf("dnsToken with an empty file = %q, %v; want the env's", token, err)
	}
	if err := os.WriteFile(path, []byte("from-the-file-0123456789abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := dnsToken(); err != nil || token != "from-the-file-0123456789abc" {
		t.Errorf("dnsToken from file = %q, %v", token, err)
	}
	t.Setenv(dnsTokenEnv, "")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dnsToken(); err == nil || !strings.Contains(err.Error(), dnsTokenEnv) {
		t.Errorf("dnsToken with an empty file and no env: %v", err)
	}
}

// The devices' store refuses a site cookie key too short to sign with.
func TestDevicesWithKeyRefusesShortKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	if _, err := devicesWithKey(path, make([]byte, 16)); err == nil {
		t.Error("a 16-byte key was taken")
	}
	if s, err := devicesWithKey(path, make([]byte, 32)); err != nil || s == nil {
		t.Errorf("a 32-byte key: %v", err)
	}
}

// The core opens the devices with the key derived from the page secret.
func TestOpenDevices(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", testutil.TempDir(t, "nh"))
	if s, err := openDevices(); err != nil || s == nil {
		t.Errorf("openDevices: %v", err)
	}
}

func TestNetworkTokenWritesSecretFile(t *testing.T) {
	configHome(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := config.DNSTokenPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeWithInput(t, "tok\n", newNetworkCmd(), "network", "token"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "tok" {
		t.Fatalf("token file = %q, %v, want tok", got, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	// A longer line, a CRLF and spaces around it are trimmed.
	if _, err := executeWithInput(t, "  tok2 \r\n", newNetworkCmd(), "network", "token"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "tok2" {
		t.Errorf("token file = %q, want tok2", got)
	}
	if _, err := executeWithInput(t, "", newNetworkCmd(), "network", "token", "--clear"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("after --clear the file is there: %v", err)
	}
	// Clearing what is not there is no failure.
	if _, err := executeWithInput(t, "", newNetworkCmd(), "network", "token", "--clear"); err != nil {
		t.Errorf("--clear with no token: %v", err)
	}
}

func TestNetworkTokenRefusesEmpty(t *testing.T) {
	configHome(t)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	out, err := executeWithInput(t, " \n", newNetworkCmd(), "network", "token")
	if err == nil {
		t.Fatalf("an empty token was taken: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "nearhost", "dns-token")); !os.IsNotExist(err) {
		t.Errorf("a file was written: %v", err)
	}
}

// A store that can't be opened is a warning on a networked core only: a
// core that is not networked has no use for devices, so it says nothing.
func TestOpenPageDevicesWarnsOnlyWhenNetworked(t *testing.T) {
	blocked := filepath.Join(testutil.TempDir(t, "nh"), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The data directory is a file, so the page secret can't be made.
	t.Setenv("XDG_DATA_HOME", blocked)
	for _, networked := range []bool{false, true} {
		var got []engine.Event
		if s := openPageDevices(networked, func(e engine.Event) { got = append(got, e) }); s != nil {
			t.Errorf("networked %v: a store opened", networked)
		}
		if networked && (len(got) != 1 || got[0].Report == nil || got[0].Report.Code != report.NetworkDevicesUnavailable) {
			t.Errorf("networked: events %+v, want the devices warning", got)
		}
		if !networked && len(got) != 0 {
			t.Errorf("not networked: events %+v, want none", got)
		}
	}
}
