//go:build windows

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/hosts"
)

func TestElevationCopyOnWindows(t *testing.T) {
	root := NewRoot()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{"hosts", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Windows asks you to approve the change.") || strings.Contains(out.String(), "sudo") {
		t.Fatalf("hosts --help:\n%s", out)
	}
	flag := NewRoot().PersistentFlags().Lookup("config")
	if flag == nil || !strings.Contains(flag.Usage, "%APPDATA%") || !strings.Contains(flag.Usage, "XDG_CONFIG_HOME") {
		t.Fatalf("--config usage %v", flag)
	}
}

func TestHostsApplyRefusesASymlinkedInputOnWindows(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("secret line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "input")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("cannot make a symlink (needs an elevated session): %v", err)
	}
	report := filepath.Join(t.TempDir(), "report")
	path, err := runApply(t, "--input", link, "--report", report)
	if err == nil {
		t.Fatal("expected an error")
	}
	data, _ := os.ReadFile(report)
	if strings.Contains(err.Error(), "secret line") || strings.Contains(string(data), "secret line") {
		t.Fatalf("leaked: err %v, report %q", err, data)
	}
	if got, _ := os.ReadFile(path); string(got) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", got)
	}
}

func TestHostsApplyInputAndReportInOneFolderOnWindows(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte("a.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := runApply(t, "--input", input, "--report", filepath.Join(dir, "report"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "127.0.0.1 a.test") {
		t.Fatalf("unexpected hosts:\n%s", got)
	}

	if err := os.WriteFile(input, []byte("bad name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = runApply(t, "--input", input, "--report", filepath.Join(dir, "report2"))
	want := hosts.Apply(filepath.Join(t.TempDir(), "x"), []string{"bad name"})
	if err == nil || want == nil || err.Error() != want.Error() {
		t.Fatalf("apply = %v; want %v", err, want)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "report2")); strings.TrimSpace(string(data)) != want.Error() {
		t.Fatalf("report %q; want %q", data, want)
	}
}

func TestHostsApplyRefusesAJunctionedFolderOnWindows(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "input"), []byte("a.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(t.TempDir(), "j")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("cannot make a junction: %v %s", err, out)
	}
	path, err := runApply(t, "--input", filepath.Join(junction, "input"))
	if err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("apply = %v; want the folder refused as a link", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", got)
	}
}

func TestHeldFolderCannotBeRenamedOnWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "held")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var dirs heldDirs
	if _, err := dirs.hold(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"2"); err == nil {
		t.Fatal("renamed a held folder")
	}
	dirs.Close()
	if err := os.Rename(dir, dir+"2"); err != nil {
		t.Fatalf("rename after Close: %v", err)
	}
}

func TestHeldFolderRefusesAFileElsewhereOnWindows(t *testing.T) {
	dir := t.TempDir()
	var dirs heldDirs
	defer dirs.Close()
	hd, err := dirs.hold(dir)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(other)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := hd.holds(windows.Handle(f.Fd()), filepath.Join(dir, "input")); err == nil {
		t.Fatal("a file in another folder passed as the held folder's")
	}
}
