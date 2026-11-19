package transport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

// copySSHTo copies the fake ssh into dir as ssh.exe and returns its path.
func copySSHTo(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(testutil.FakeSSH(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ssh.exe")
	if err := os.WriteFile(path, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSSHIsFoundOnceOnWindows checks that the ssh.exe found on PATH at first
// use is the one later commands run after PATH names another, and that a
// failed lookup is not kept.
func TestSSHIsFoundOnceOnWindows(t *testing.T) {
	resetSSHPath(t)
	ctx := context.Background()
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	if err := SSHCommand(ctx, "", nil, "-V").Run(); err == nil || err.Error() != exec.Command("ssh").Err.Error() {
		t.Fatalf("Run() with no ssh = %v", err)
	}

	first := copySSHTo(t, t.TempDir())
	t.Setenv("PATH", filepath.Dir(first))
	if cmd := newPerProcess(Config{Name: "mini", Host: "mini"}).command(ctx, "-V"); !strings.EqualFold(cmd.Path, first) {
		t.Fatalf("first command runs %q, want %q", cmd.Path, first)
	}
	other := copySSHTo(t, t.TempDir())
	t.Setenv("PATH", filepath.Dir(other))
	if cmd := newPerProcess(Config{Name: "mini", Host: "mini"}).command(ctx, "-V"); !strings.EqualFold(cmd.Path, first) {
		t.Fatalf("after PATH changed the command runs %q, want %q", cmd.Path, first)
	}
}
