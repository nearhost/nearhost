package cli

import (
	"bytes"
	"errors"
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

// noElevation makes the elevated helper fail the test if anything starts it.
func noElevation(t *testing.T) {
	t.Helper()
	orig := newElevatedApplier
	t.Cleanup(func() { newElevatedApplier = orig })
	newElevatedApplier = func(exe, p string) hosts.Applier {
		t.Errorf("the elevated helper was started for %s", p)
		return func([]string) error { return errors.New("elevated") }
	}
}

// Clean writes a file other than the system hosts file in this process, with
// no elevation: the elevated helper would refuse it.
func TestHostsCleanWritesAnotherFileWithoutElevation(t *testing.T) {
	noElevation(t)
	path := filepath.Join(t.TempDir(), "hosts")
	content := "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end\n"
	os.WriteFile(path, []byte(content), 0o644)
	root := NewRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"hosts", "clean", "--file", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("unexpected hosts:\n%s", data)
	}
}

// Clean hands the system hosts file to the elevated helper.
func TestHostsCleanElevatesForTheSystemFile(t *testing.T) {
	orig := newElevatedApplier
	t.Cleanup(func() { newElevatedApplier = orig })
	var gotPath string
	newElevatedApplier = func(exe, p string) hosts.Applier {
		gotPath = p
		return func([]string) error { return nil }
	}
	apply, err := cleanApplier(hosts.DefaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(nil); err != nil || gotPath != hosts.DefaultPath {
		t.Fatalf("err %v, helper got %q, want %q", err, gotPath, hosts.DefaultPath)
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

// runElevated makes the hosts commands see an elevated process until the
// test ends. TestMain makes them see an unelevated one, whatever the runner
// is, as CI's Windows runner is elevated.
func runElevated(t *testing.T) {
	t.Helper()
	orig := isElevated
	isElevated = func() bool { return true }
	t.Cleanup(func() { isElevated = orig })
}

// An elevated apply writes no file but the system hosts file: the caller
// chooses --file, and the helper runs as root or administrator.
func TestElevatedHostsApplyRefusesAnotherFile(t *testing.T) {
	runElevated(t)
	report := filepath.Join(t.TempDir(), "report")
	path, err := runApply(t, "--report", report)
	want := "an elevated run only writes " + hosts.SystemPath()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if data, _ := os.ReadFile(path); string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", data)
	}
	if data, _ := os.ReadFile(report); strings.TrimSpace(string(data)) != want {
		t.Fatalf("report %q, want %q", data, want)
	}
}

// A link to another file is refused by its path, as the helper writes
// through links.
func TestElevatedHostsApplyRefusesALinkToAnotherFile(t *testing.T) {
	runElevated(t)
	target := writeTemp(t, "127.0.0.1 localhost\n")
	link := filepath.Join(t.TempDir(), "hosts")
	testutil.Symlink(t, target, link)
	root := NewRoot()
	root.SetIn(strings.NewReader("a.test\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"hosts", "apply", "--file", link})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "an elevated run only writes") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "127.0.0.1 localhost\n" {
		t.Fatalf("file modified:\n%s", data)
	}
}

// An elevated clean refuses another file before it reads it or starts the
// helper.
func TestElevatedHostsCleanRefusesAnotherFile(t *testing.T) {
	runElevated(t)
	path := filepath.Join(t.TempDir(), "hosts")
	content := "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end\n"
	os.WriteFile(path, []byte(content), 0o644)
	orig := newElevatedApplier
	t.Cleanup(func() { newElevatedApplier = orig })
	called := false
	newElevatedApplier = func(exe, p string) hosts.Applier {
		return func(aliases []string) error { called = true; return hosts.Apply(p, aliases) }
	}
	root := NewRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"hosts", "clean", "--file", path})
	want := "an elevated run only writes " + hosts.SystemPath()
	if err := root.Execute(); err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if called {
		t.Fatal("the helper ran")
	}
	if data, _ := os.ReadFile(path); string(data) != content {
		t.Fatalf("file modified:\n%s", data)
	}
}

// Without --file an elevated clean works on the system hosts file. The
// helper is replaced, so the real file is only read.
func TestElevatedHostsCleanWithoutFileUsesTheSystemFile(t *testing.T) {
	runElevated(t)
	orig := newElevatedApplier
	t.Cleanup(func() { newElevatedApplier = orig })
	newElevatedApplier = func(exe, p string) hosts.Applier {
		return func([]string) error { return nil }
	}
	out := &bytes.Buffer{}
	root := NewRoot()
	root.SetOut(out)
	root.SetArgs([]string{"hosts", "clean"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), hosts.DefaultPath) {
		t.Fatalf("output %q does not name %s", out, hosts.DefaultPath)
	}
}

// The elevated check passes the path Nearhost hands the helper.
func TestElevatedFileCheckAcceptsTheSystemFile(t *testing.T) {
	runElevated(t)
	for _, p := range []string{hosts.DefaultPath, hosts.SystemPath()} {
		if err := checkElevatedFile(p); err != nil {
			t.Errorf("checkElevatedFile(%q) = %v", p, err)
		}
	}
}

// An unelevated run writes any file it is given, as tests and a hand-run
// apply do.
func TestUnelevatedFileCheckAcceptsAnyFile(t *testing.T) {
	if err := checkElevatedFile(filepath.Join(t.TempDir(), "hosts")); err != nil {
		t.Fatal(err)
	}
}
