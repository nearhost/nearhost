package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/hosts"
)

func TestHostsApplyReadsAliasesFromStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644)
	root := NewRoot()
	root.SetIn(strings.NewReader("a.test\n\nb.test\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"hosts", "apply", "--file", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "127.0.0.1 a.test\n127.0.0.1 b.test") {
		t.Fatalf("unexpected hosts:\n%s", data)
	}
}

func TestHostsCleanPassesFilePathToApplier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	content := "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n# nearhost:end\n"
	os.WriteFile(path, []byte(content), 0o644)
	var gotPath string
	orig := newSudoApplier
	t.Cleanup(func() { newSudoApplier = orig })
	newSudoApplier = func(exe, p string) hosts.Applier {
		gotPath = p
		return func(aliases []string) error { return hosts.Apply(p, aliases) }
	}
	root := NewRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"hosts", "clean", "--file", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("applier got path %q, want %q", gotPath, path)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("unexpected hosts:\n%s", data)
	}
}

func TestHostsApplyRejectsInvalidAliasAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644)
	root := NewRoot()
	root.SetIn(strings.NewReader("a.test\nx.test bank.com\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"hosts", "apply", "--file", path})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "x.test bank.com") {
		t.Fatalf("err=%v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", data)
	}
}

func TestHostsSubcommandsRejectPositionalArguments(t *testing.T) {
	orig := newSudoApplier
	t.Cleanup(func() { newSudoApplier = orig })
	newSudoApplier = func(exe, p string) hosts.Applier {
		return func(aliases []string) error { return hosts.Apply(p, aliases) }
	}
	for _, sub := range []string{"apply", "clean"} {
		path := filepath.Join(t.TempDir(), "hosts")
		content := "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n# nearhost:end\n"
		os.WriteFile(path, []byte(content), 0o644)
		root := NewRoot()
		root.SetIn(strings.NewReader(""))
		root.SetOut(&bytes.Buffer{})
		root.SetArgs([]string{"hosts", sub, "--file", path, "/tmp/other-hosts"})
		if err := root.Execute(); err == nil {
			t.Errorf("hosts %s: expected an error for a positional argument", sub)
		}
		if data, _ := os.ReadFile(path); string(data) != content {
			t.Errorf("hosts %s: file modified:\n%s", sub, data)
		}
	}
}
