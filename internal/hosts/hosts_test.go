package hosts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/sudo"
)

const base = "127.0.0.1\tlocalhost\n255.255.255.255\tbroadcasthost\n::1             localhost\n"

func TestRewriteAppendsBlockWhenAbsent(t *testing.T) {
	got := mustRewrite(t, base, []string{"b.test", "a.test"})
	want := base + "# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test\n::1 b.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteWithoutTrailingNewlineStartsBlockOnOwnLine(t *testing.T) {
	got := mustRewrite(t, "127.0.0.1\tlocalhost", []string{"a.test"})
	if !strings.HasPrefix(got, "127.0.0.1\tlocalhost\n# nearhost:start\n") {
		t.Fatalf("marker merged into last line:\n%s", got)
	}
	if !strings.HasSuffix(got, "# nearhost:end\n") {
		t.Fatalf("missing trailing newline:\n%s", got)
	}
}

func TestRewriteReplacesStaleBlockAndKeepsSurroundings(t *testing.T) {
	existing := "# before\n" + base + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end\n# after\n192.168.1.5 nas\n"
	got := mustRewrite(t, existing, []string{"new.test"})
	want := "# before\n" + base + "# nearhost:start\n127.0.0.1 new.test\n::1 new.test\n# nearhost:end\n# after\n192.168.1.5 nas\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteWithNoAliasesRemovesBlock(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end\n"
	if got := mustRewrite(t, existing, nil); got != base {
		t.Fatalf("got:\n%s", got)
	}
	if got := mustRewrite(t, "# nearhost:start\n127.0.0.1 x.test\n# nearhost:end\n", nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRewriteDeduplicatesAliases(t *testing.T) {
	got := mustRewrite(t, "", []string{"a.test", "a.test"})
	if strings.Count(got, "127.0.0.1 a.test") != 1 || strings.Count(got, "::1 a.test") != 1 {
		t.Fatalf("duplicate alias written:\n%s", got)
	}
}

func TestRewriteIsIdempotent(t *testing.T) {
	once := mustRewrite(t, base, []string{"a.test"})
	if twice := mustRewrite(t, once, []string{"a.test"}); twice != once {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

func TestAliasesParsesBlock(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 a.test\n127.0.0.1 b.test\n# nearhost:end\n"
	if got := Aliases(existing); !reflect.DeepEqual(got, []string{"a.test", "b.test"}) {
		t.Fatalf("got %v", got)
	}
	if got := Aliases(base); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestApplyWritesFileInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte(base), 0o644)
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "127.0.0.1 a.test") {
		t.Fatalf("not written:\n%s", data)
	}
}

func TestEnsureCallsApplierOnlyWhenChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte(base), 0o644)
	var calls [][]string
	apply := func(aliases []string) error {
		calls = append(calls, aliases)
		return Apply(path, aliases)
	}
	changed, err := Ensure(path, []string{"a.test"}, apply)
	if err != nil || !changed || len(calls) != 1 {
		t.Fatalf("first ensure: changed=%v err=%v calls=%v", changed, err, calls)
	}
	changed, err = Ensure(path, []string{"a.test"}, apply)
	if err != nil || changed || len(calls) != 1 {
		t.Fatalf("second ensure should be a no-op: changed=%v err=%v calls=%v", changed, err, calls)
	}
}

func TestEnsureTreatsMissingFileAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	changed, err := Ensure(path, []string{"a.test"}, func([]string) error { return nil })
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestRewriteUnterminatedBlockKeepsForeignContent(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n10.0.0.1 nas\n# keep me\n127.0.0.1 mine.test\n"
	got := mustRewrite(t, existing, []string{"new.test"})
	want := base + "10.0.0.1 nas\n# keep me\n127.0.0.1 mine.test\n# nearhost:start\n127.0.0.1 new.test\n::1 new.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if Aliases(existing) != nil {
		t.Fatalf("unterminated block should yield no aliases")
	}
}

func TestRewriteUnterminatedBlockRemovedWhenNoAliases(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n10.0.0.1 nas\n"
	if got, want := mustRewrite(t, existing, nil), base+"10.0.0.1 nas\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteBlockLastWithoutTrailingNewline(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end"
	got := mustRewrite(t, existing, []string{"new.test"})
	want := base + "# nearhost:start\n127.0.0.1 new.test\n::1 new.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCleanLeavesRestUntouched(t *testing.T) {
	existing := "# before\n" + base + "# nearhost:start\n127.0.0.1 a.test\n# nearhost:end\n# after\n"
	if got, want := mustRewrite(t, existing, nil), "# before\n"+base+"# after\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnsureNoApplyWhenBlockCorrectWithoutTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte("127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end"), 0o644)
	called := false
	changed, err := Ensure(path, []string{"a.test"}, func([]string) error { called = true; return nil })
	if err != nil || changed || called {
		t.Fatalf("changed=%v err=%v called=%v", changed, err, called)
	}
}

// useSudo makes path the sudo that Nearhost runs, root-owned or not. The
// file must exist and be a regular file.
func useSudo(t *testing.T, path string) {
	t.Helper()
	origCandidates, origOwned := sudo.Candidates, sudo.OwnedByRoot
	sudo.Candidates = []string{path}
	sudo.OwnedByRoot = func(fs.FileInfo) bool { return true }
	t.Cleanup(func() { sudo.Candidates, sudo.OwnedByRoot = origCandidates, origOwned })
}

func TestSudoCommandCarriesFilePathAndAliases(t *testing.T) {
	fakeSudo := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(fakeSudo, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fakeSudo)
	cmd, err := sudoCommand(context.Background(), "/bin/nearhost", "/tmp/other-hosts", sudoPrompt, false, []string{"a.test", "b.test"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	if !strings.HasSuffix(args, "/bin/nearhost hosts apply --file /tmp/other-hosts") {
		t.Fatalf("args: %s", args)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if string(in) != "a.test\nb.test\n" {
		t.Fatalf("stdin: %q", in)
	}
}

func mustRewrite(t *testing.T, existing string, aliases []string) string {
	t.Helper()
	got, err := Rewrite(existing, aliases)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRewriteRejectsInvalidAliases(t *testing.T) {
	bad := []string{"x.test bank.com", "a.test #", "A.test", "-a.test", "a-.test", ".test", "test",
		"a.test\nb.com", "a\x07.test", "a..test", "a_b.test", strings.Repeat("a", 64) + ".test",
		strings.Repeat("a.", 130) + "test"}
	for _, a := range bad {
		if got, err := Rewrite(base, []string{"ok.test", a}); err == nil || got != "" {
			t.Errorf("alias %q accepted: err=%v got=%q", a, err, got)
		} else if !strings.Contains(err.Error(), fmt.Sprintf("%q", a)) {
			t.Errorf("error does not name %q: %v", a, err)
		}
	}
}

func TestRewriteAcceptsValidAliases(t *testing.T) {
	got := mustRewrite(t, "", []string{"my-app-2.test", "api.myapp.test", "  pad.test  "})
	want := "# nearhost:start\n127.0.0.1 api.myapp.test\n::1 api.myapp.test\n127.0.0.1 my-app-2.test\n::1 my-app-2.test\n127.0.0.1 pad.test\n::1 pad.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRewriteWhitespaceOnlyAliasesWriteNoBlock(t *testing.T) {
	if got := mustRewrite(t, base, []string{"  ", "\t"}); got != base {
		t.Fatalf("got:\n%s", got)
	}
}

func TestApplyAndEnsureRejectInvalidWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte(base), 0o644)
	if err := Apply(path, []string{"x.test bank.com"}); err == nil {
		t.Fatal("Apply accepted invalid alias")
	}
	called := false
	if _, err := Ensure(path, []string{"Evil.com"}, func([]string) error { called = true; return nil }); err == nil || called {
		t.Fatalf("Ensure err=%v applier called=%v", err, called)
	}
	if data, _ := os.ReadFile(path); string(data) != base {
		t.Fatalf("file modified:\n%s", data)
	}
}

func TestRewriteUnterminatedBlockKeepsForeignLoopbackEntries(t *testing.T) {
	existing := "# nearhost:start\n127.0.0.1 a.test\n127.0.0.1 localhost\n127.0.0.1 b.test\n"
	got, err := Rewrite(existing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "127.0.0.1 localhost\n127.0.0.1 b.test\n"; got != want {
		t.Fatalf("only .test entries directly after the marker belong to the block:\n%q\nwant\n%q", got, want)
	}
}

func TestSudoApplierContextStopsSudoWhenCancelled(t *testing.T) {
	// A stand-in for sudo that, like sudo at its password prompt, does not
	// finish by itself.
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n: > '"+started+"'\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- SudoApplierContext(ctx, "/bin/nearhost", filepath.Join(dir, "hosts"))([]string{"a.test"})
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in for sudo never started")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("applier returned %v, want the context's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the applier kept waiting for sudo after its context was cancelled")
	}
}

func TestSudoApplierContextDoesNotStartSudoWhenAlreadyCancelled(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n: > '"+started+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SudoApplierContext(ctx, "/bin/nearhost", filepath.Join(dir, "hosts"))([]string{"a.test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("applier returned %v, want the context's error", err)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("sudo was started for a cancelled context")
	}
}

func tempLeftovers(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory holds %v, want %v", got, want)
	}
}

func TestApplyWritesCorrectContentAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(path, []string{"b.test", "a.test"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := base + "# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test\n::1 b.test\n# nearhost:end\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	tempLeftovers(t, dir, "hosts")
}

func TestApplyPreservesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o600} {
		dir := t.TempDir()
		path := filepath.Join(dir, "hosts")
		if err := os.WriteFile(path, []byte(base), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := Apply(path, []string{"a.test"}); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Fatalf("mode %v, want %v", fi.Mode().Perm(), mode)
		}
	}
}

func TestApplyCreatesMissingFileWith0644(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, want 0644", fi.Mode().Perm())
	}
	got, _ := os.ReadFile(path)
	if string(got) != "# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end\n" {
		t.Fatalf("got %q", got)
	}
	tempLeftovers(t, dir, "hosts")
}

func TestApplyThroughSymlinkedFileKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "hosts")
	if err := os.WriteFile(real, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	if err := Apply(link, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", fi, err)
	}
	got, _ := os.ReadFile(real)
	if !strings.Contains(string(got), "127.0.0.1 a.test") {
		t.Fatalf("target not updated:\n%s", got)
	}
	viaLink, _ := os.ReadFile(link)
	if string(viaLink) != string(got) {
		t.Fatal("link does not show updated content")
	}
	tempLeftovers(t, dir, "hosts", "real")
}

func TestApplyThroughSymlinkedParentDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "private")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private", filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "hosts"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(filepath.Join(root, "etc", "hosts"), []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "etc")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("parent symlink replaced: %v %v", fi, err)
	}
	got, _ := os.ReadFile(filepath.Join(realDir, "hosts"))
	if !strings.Contains(string(got), "127.0.0.1 a.test") {
		t.Fatalf("target not updated:\n%s", got)
	}
	tempLeftovers(t, realDir, "hosts")
}

func TestApplyFailedRenameLeavesOriginalIntactAndNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	renameFile = func(oldpath, newpath string) error {
		// The original must be whole while the new content waits in the temp file.
		if got, _ := os.ReadFile(path); string(got) != base {
			t.Errorf("original changed before rename: %q", got)
		}
		return errors.New("boom")
	}
	err := Apply(path, []string{"a.test"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want rename failure", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != base {
		t.Fatalf("original changed: %q", got)
	}
	tempLeftovers(t, dir, "hosts")
}

func TestApplyNothingToDoWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	renameFile = func(string, string) error {
		t.Error("rename called on the fast path")
		return nil
	}
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if !fi.ModTime().Equal(before.ModTime()) {
		t.Fatalf("mtime changed: %v, was %v", fi.ModTime(), before.ModTime())
	}
	tempLeftovers(t, dir, "hosts")
}

func TestApplyUnwritableDirectoryNamesDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := Apply(path, []string{"a.test"})
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("err = %v, want one naming %s", err, dir)
	}
	got, _ := os.ReadFile(path)
	if string(got) != base {
		t.Fatalf("file changed despite error: %q", got)
	}
}

// A symlink whose target does not exist must not be replaced by a regular
// file, which is what renaming over the link itself would do. Apply refuses
// and says which link points where.
func TestApplyRefusesADanglingSymlinkAndLeavesIt(t *testing.T) {
	for _, dest := range []string{"missing", filepath.Join(t.TempDir(), "elsewhere", "hosts")} {
		dir := t.TempDir()
		link := filepath.Join(dir, "hosts")
		if err := os.Symlink(dest, link); err != nil {
			t.Fatal(err)
		}
		err := Apply(link, []string{"a.test"})
		if err == nil {
			t.Fatalf("Apply through a link to %s succeeded", dest)
		}
		for _, want := range []string{link, dest, "symbolic link", "does not exist"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
		if fi, lerr := os.Lstat(link); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced: %v %v", fi, lerr)
		}
		if got, _ := os.Readlink(link); got != dest {
			t.Errorf("the link now points to %q", got)
		}
		if _, serr := os.Stat(link); serr == nil {
			t.Error("the link's target was created")
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("the directory holds %d entries, want the link alone", len(entries))
		}
	}
}

// The applier can run sudo so it never prompts, for credentials already
// cached, or prompt with words of the caller's.
func TestSudoApplierWith(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$*\" > '"+args+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	ctx := context.Background()
	for _, c := range []struct {
		prompt   string
		noPrompt bool
		want     string
	}{
		{"", true, "-n /bin/nearhost hosts apply --file /etc/hosts\n"},
		{"Password for %p: ", false, "-p Password for %p:  /bin/nearhost hosts apply --file /etc/hosts\n"},
	} {
		if err := SudoApplierWith(ctx, "/bin/nearhost", "/etc/hosts", c.prompt, c.noPrompt)([]string{"a.test"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(args); string(got) != c.want {
			t.Errorf("sudo %q, want %q", got, c.want)
		}
	}
}

// Without a prompt the applier runs under a terminal in use: what sudo or
// the command says goes into the error, not onto the screen.
func TestSudoApplierWithoutAPromptKeepsTheTerminalClear(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "sudo")
	script := "#!/bin/sh\necho 'some output'\necho 'sudo: a password is required' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	err := SudoApplierWith(context.Background(), "/bin/nearhost", "/etc/hosts", "", true)([]string{"a.test"})
	os.Stdout, os.Stderr = stdout, stderr
	outW.Close()
	errW.Close()
	shown, _ := io.ReadAll(io.MultiReader(outR, errR))
	if len(shown) > 0 {
		t.Errorf("reached the terminal: %q", shown)
	}
	if err == nil || !strings.Contains(err.Error(), "a password is required") {
		t.Errorf("err = %v", err)
	}
}

func TestRewriteAcceptsRealDomainsButNotPatternsOrLocalhost(t *testing.T) {
	if _, err := Rewrite("", []string{"a.test", "app.local.example.io"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"localhost", "*.local.example.io", "App.example.io"} {
		if _, err := Rewrite("", []string{bad}); err == nil {
			t.Errorf("Rewrite accepted %q", bad)
		}
	}
}

func TestBlockWritesIPv4AndIPv6(t *testing.T) {
	got := mustRewrite(t, "", []string{"b.test", "a.test"})
	want := "# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test\n::1 b.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAliasesDedupesBothFamilies(t *testing.T) {
	got := Aliases("# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test\n::1 b.test\n# nearhost:end\n")
	if want := []string{"a.test", "b.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAliasesReadsOldIPv4OnlyBlock(t *testing.T) {
	got := Aliases("# nearhost:start\n127.0.0.1 a.test\n127.0.0.1 b.test\n# nearhost:end\n")
	if want := []string{"a.test", "b.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEnsureRewritesOldBlockOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	old := base + "# nearhost:start\n127.0.0.1 a.test\n127.0.0.1 b.test\n# nearhost:end\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	aliases := []string{"a.test", "b.test"}
	apply := func(a []string) error { return Apply(path, a) }
	if changed, err := Ensure(path, aliases, apply); err != nil || !changed {
		t.Fatalf("first Ensure: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(path)
	want := base + "# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n127.0.0.1 b.test\n::1 b.test\n# nearhost:end\n"
	if string(data) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", data, want)
	}
	if changed, err := Ensure(path, aliases, apply); err != nil || changed {
		t.Fatalf("second Ensure: changed=%v err=%v", changed, err)
	}
}
