package firewall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
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

// netshCmd is netsh with args, as written, with no window: the background
// core has no console, so netsh gets one of its own, which
// CREATE_NO_WINDOW keeps from showing, as SW_HIDE does not reliably.
func netshCmd(ctx context.Context, args []string) *exec.Cmd {
	path := netshPath()
	cmd := exec.CommandContext(ctx, path)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: commandLine(path, args), CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// Netsh runs netsh with args, as written, gives up after netshTimeout, and
// returns what it printed as UTF-8: into a pipe netsh writes in the OEM
// code page, so a path with a letter outside ASCII would not match.
func Netsh(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netshTimeout)
	defer cancel()
	out, err := netshCmd(ctx, args).CombinedOutput()
	if text, derr := decodeOEM(oemCP(), out); derr == nil {
		out = text
	}
	return out, err
}

// getOEMCP is kernel32's GetOEMCP, which x/sys/windows does not wrap.
var getOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

// oemCP is the system's OEM code page.
func oemCP() uint32 {
	cp, _, _ := getOEMCP.Call()
	return uint32(cp)
}

// decodeOEM is b, text in code page cp, as UTF-8.
func decodeOEM(cp uint32, b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil {
		return nil, err
	}
	wide := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &wide[0], n); err != nil {
		return nil, err
	}
	return []byte(string(utf16.Decode(wide))), nil
}
