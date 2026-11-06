//go:build windows

package hosts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
)

// TestElevatedApplyEndToEndOnWindows builds nearhost.exe and applies a block
// through the UAC elevator. From an elevated session (CI, ssh) the launch
// shows no prompt; from a normal one it asks, so run it from an elevated
// terminal.
func TestElevatedApplyEndToEndOnWindows(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "nearhost.exe")
	build := exec.Command("go", "build", "-o", exe, "github.com/nearhost/nearhost/cmd/nearhost")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Ensure(path, []string{"a.test"}, ElevatedApplier(context.Background(), elevate.System(), exe, path, "", false))
	if err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "127.0.0.1 a.test") {
		t.Fatalf("hosts:\n%s", data)
	}
}
