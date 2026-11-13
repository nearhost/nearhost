//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// A config that cannot be saved stops add before discovery when the answer
// is don't ask again.
// A chmod to 0o555 does not stop writes on Windows, so the config cannot be
// made unsaveable this way there.
func TestAddFailsWhenDontAskAgainCannotBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	cfgPath, _, b := asking(t, true)
	saveConfig(t, cfgPath, &config.Config{})
	dir := filepath.Dir(cfgPath)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	out, err := addSailCustom(t, b, cfgPath, "d\n", "myapp", "--web", "8080")
	// The lock beside the config cannot be created either, so that is
	// where the save stops.
	if err == nil || !strings.Contains(err.Error(), "open config lock") {
		t.Fatalf("expected the save error, got %v\n%s", err, out)
	}
	if strings.Contains(out, "Routes found") {
		t.Errorf("discovery ran:\n%s", out)
	}
}
