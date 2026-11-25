package firewall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Needed is whether this OS needs a firewall rule for networked mode.
const Needed = true

// netshTimeout is how long one netsh run may take.
const netshTimeout = 5 * time.Second

// Ready reports whether Nearhost's rule is there for the running program.
// It runs netsh, so it may take up to netshTimeout: callers keep it out
// of any lock.
func Ready() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return ReadyWith(Netsh, exe)
}

// netshPath is netsh in the system folder, never one found on PATH.
func netshPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "netsh.exe")
}

// commandLine is the command line that runs path with args as written:
// netsh reads name="Nearhost" and program="C:\..." itself, which Go's
// quoting of each argument would break.
func commandLine(path string, args []string) string {
	return `"` + path + `" ` + strings.Join(args, " ")
}

// Netsh runs netsh with args, as written, and gives up after
// netshTimeout.
func Netsh(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netshTimeout)
	defer cancel()
	path := netshPath()
	cmd := exec.CommandContext(ctx, path)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: commandLine(path, args), HideWindow: true}
	return cmd.CombinedOutput()
}
