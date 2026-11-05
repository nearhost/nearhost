//go:build windows

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if flag == nil || !strings.Contains(flag.Usage, "%APPDATA%") {
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
