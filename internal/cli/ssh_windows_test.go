//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// Windows has no /tmp, so a private session's run directory goes under the
// user's temporary directory.
func TestPrivateSessionRunDirIsUnderTempDirOnWindows(t *testing.T) {
	_, runDir, end, err := sshClient{}.privateSession(config.Server{Name: "leto", SSHHost: "leto"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(runDir, filepath.Clean(os.TempDir())+string(filepath.Separator)) {
		t.Fatalf("run directory %q is not under %q", runDir, os.TempDir())
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run directory: %v", err)
	}
	if err := end(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("run directory remains after end: %v", err)
	}
}
