//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirefoxRootsOffOnWindows(t *testing.T) {
	appdata := t.TempDir()
	profile := filepath.Join(appdata, "Mozilla", "Firefox", "Profiles", "abc.default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, off := firefoxProfilesOff(appdata); off {
		t.Fatal("off with no preference set")
	}
	prefs := `user_pref("security.enterprise_roots.enabled", false);` + "\n"
	if err := os.WriteFile(filepath.Join(profile, "prefs.js"), []byte(prefs), 0o644); err != nil {
		t.Fatal(err)
	}
	where, off := firefoxProfilesOff(appdata)
	if !off || !strings.Contains(where, "abc.default") {
		t.Fatalf("where = %q, off = %v", where, off)
	}
}
