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
	"github.com/nearhost/nearhost/internal/testutil"
)

// TestElevatedApplyRefusesAnotherFileOnWindows builds nearhost.exe and
// applies a block to a file in a temp directory through the UAC elevator.
// The helper runs elevated, so it refuses any file but the system hosts
// file, says so through its report, and leaves the file as it was. From an
// elevated session (CI, ssh) the launch shows no prompt; from a normal one it
// asks, so run it from an elevated terminal.
func TestElevatedApplyRefusesAnotherFileOnWindows(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "nearhost.exe")
	build := exec.Command("go", "build", "-o", exe, "github.com/nearhost/nearhost/cmd/nearhost")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	path := filepath.Join(dir, "hosts")
	const content = "127.0.0.1 localhost\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Ensure(path, []string{"a.test"}, ElevatedApplier(context.Background(), elevate.System(), exe, path, "", false))
	want := "an elevated run only writes " + SystemPath()
	if err == nil || changed || !strings.Contains(err.Error(), want) {
		t.Fatalf("changed %v, err %v, want an error with %q", changed, err, want)
	}
	if data, _ := os.ReadFile(path); string(data) != content {
		t.Fatalf("hosts:\n%s", data)
	}
}

// Windows has no directory permission bits that stop a write, but a file with
// the read-only attribute refuses to be replaced. Apply must say which file it
// could not write and leave it as it was.
func TestApplyReadOnlyFileNamesTheFileOnWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	const content = "127.0.0.1 localhost\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	// A read-only file cannot be removed with the folder that holds it.
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	err := Apply(path, []string{"a.test"})
	// The error names the long form of the path; the runner's TEMP is 8.3.
	long := testutil.LongPath(t, path)
	if err == nil || !strings.Contains(err.Error(), long) {
		t.Fatalf("err = %v, want one naming %s", err, long)
	}
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Fatalf("file changed despite error: %q", got)
	}
	tempLeftovers(t, dir, "hosts")
}
