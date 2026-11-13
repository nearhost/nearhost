package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/testutil"
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
	if !strings.Contains(string(data), "127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test") {
		t.Fatalf("unexpected hosts:\n%s", data)
	}
}

func TestHostsCleanPassesFilePathToApplier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	content := "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end\n"
	os.WriteFile(path, []byte(content), 0o644)
	var gotPath string
	orig := newElevatedApplier
	t.Cleanup(func() { newElevatedApplier = orig })
	newElevatedApplier = func(exe, p string) hosts.Applier {
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
	orig := newElevatedApplier
	t.Cleanup(func() { newElevatedApplier = orig })
	newElevatedApplier = func(exe, p string) hosts.Applier {
		return func(aliases []string) error { return hosts.Apply(p, aliases) }
	}
	for _, sub := range []string{"apply", "clean"} {
		path := filepath.Join(t.TempDir(), "hosts")
		content := "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end\n"
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

// runApply runs `hosts apply` on a hosts file holding localhost, with the
// args after --file, and returns the file's path and apply's error.
func runApply(t *testing.T, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	root.SetIn(strings.NewReader("stdin.test\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"hosts", "apply", "--file", path}, args...))
	return path, root.Execute()
}

// writeTemp writes content to a new file in a temp directory and returns its
// path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHostsApplyReadsAnInputFile(t *testing.T) {
	path, err := runApply(t, "--input", writeTemp(t, "a.test\nb.test\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, "127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test") || strings.Contains(got, "stdin.test") {
		t.Fatalf("unexpected hosts:\n%s", got)
	}
}

func TestHostsApplyWritesAReport(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report")
	path, err := runApply(t, "--input", writeTemp(t, "bad name\n"), "--report", report)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := hosts.Apply(filepath.Join(t.TempDir(), "x"), []string{"bad name"})
	if want == nil || want.Error() != err.Error() {
		t.Fatalf("err %v, hosts.Apply says %v", err, want)
	}
	data, rerr := os.ReadFile(report)
	if rerr != nil || strings.TrimSpace(string(data)) != err.Error() {
		t.Fatalf("report %q (%v), want %q", data, rerr, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", data)
	}
}

func TestHostsApplyDoesNotWriteAReportOverAnExistingFile(t *testing.T) {
	report := writeTemp(t, "keep\n")
	if _, err := runApply(t, "--input", writeTemp(t, "bad name\n"), "--report", report); err == nil {
		t.Fatal("expected an error")
	}
	if data, _ := os.ReadFile(report); string(data) != "keep\n" {
		t.Fatalf("report overwritten: %q", data)
	}
}

func TestHostsApplyRefusesAHostileInputFile(t *testing.T) {
	in := writeTemp(t, "a.test\n# nearhost:end\n10.0.0.1 evil.test\nevil.test 127.0.0.1\n")
	path, err := runApply(t, "--input", in)
	if err == nil {
		t.Fatal("expected an error")
	}
	if data, _ := os.ReadFile(path); string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", data)
	}
}

func TestHostsApplyInputOverOneMiB(t *testing.T) {
	line := strings.Repeat("a", 1023) + "\n"
	over := strings.Repeat(line, 1024) + "x" // 1 MiB + 1 byte
	path, err := runApply(t, "--input", writeTemp(t, over))
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("err %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", data)
	}
	if len(over) != 1<<20+1 {
		t.Fatalf("test input is %d bytes", len(over))
	}
}

func TestHostsHelpUsesTheElevationCopy(t *testing.T) {
	root := NewRoot()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{"hosts", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), elevationNote) || !strings.Contains(out.String(), cleanShort) {
		t.Fatalf("help:\n%s", out)
	}
}

func TestHostsApplyRefusesASymlinkedInput(t *testing.T) {
	secret := writeTemp(t, "secret line\n")
	link := filepath.Join(t.TempDir(), "input")
	testutil.Symlink(t, secret, link)
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

func TestHostsApplyRefusesAnInputInALinkedDirectory(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "in"), []byte("secret line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "dir")
	testutil.Symlink(t, real, link)
	if _, err := runApply(t, "--input", filepath.Join(link, "in")); err == nil || strings.Contains(err.Error(), "secret line") {
		t.Fatalf("err %v", err)
	}
}
