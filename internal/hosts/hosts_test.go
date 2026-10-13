package hosts

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const base = "127.0.0.1\tlocalhost\n255.255.255.255\tbroadcasthost\n::1             localhost\n"

func TestRewriteAppendsBlockWhenAbsent(t *testing.T) {
	got := Rewrite(base, []string{"b.test", "a.test"})
	want := base + "# nearhost:start\n127.0.0.1 a.test\n127.0.0.1 b.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteWithoutTrailingNewlineStartsBlockOnOwnLine(t *testing.T) {
	got := Rewrite("127.0.0.1\tlocalhost", []string{"a.test"})
	if !strings.HasPrefix(got, "127.0.0.1\tlocalhost\n# nearhost:start\n") {
		t.Fatalf("marker merged into last line:\n%s", got)
	}
	if !strings.HasSuffix(got, "# nearhost:end\n") {
		t.Fatalf("missing trailing newline:\n%s", got)
	}
}

func TestRewriteReplacesStaleBlockAndKeepsSurroundings(t *testing.T) {
	existing := "# before\n" + base + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end\n# after\n192.168.1.5 nas\n"
	got := Rewrite(existing, []string{"new.test"})
	want := "# before\n" + base + "# nearhost:start\n127.0.0.1 new.test\n# nearhost:end\n# after\n192.168.1.5 nas\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteWithNoAliasesRemovesBlock(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end\n"
	if got := Rewrite(existing, nil); got != base {
		t.Fatalf("got:\n%s", got)
	}
	if got := Rewrite("# nearhost:start\n127.0.0.1 x.test\n# nearhost:end\n", nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRewriteDeduplicatesAliases(t *testing.T) {
	got := Rewrite("", []string{"a.test", "a.test"})
	if strings.Count(got, "a.test") != 1 {
		t.Fatalf("duplicate alias written:\n%s", got)
	}
}

func TestRewriteIsIdempotent(t *testing.T) {
	once := Rewrite(base, []string{"a.test"})
	if twice := Rewrite(once, []string{"a.test"}); twice != once {
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
	got := Rewrite(existing, []string{"new.test"})
	want := base + "10.0.0.1 nas\n# keep me\n127.0.0.1 mine.test\n# nearhost:start\n127.0.0.1 new.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if Aliases(existing) != nil {
		t.Fatalf("unterminated block should yield no aliases")
	}
}

func TestRewriteUnterminatedBlockRemovedWhenNoAliases(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n10.0.0.1 nas\n"
	if got, want := Rewrite(existing, nil), base+"10.0.0.1 nas\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriteBlockLastWithoutTrailingNewline(t *testing.T) {
	existing := base + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end"
	got := Rewrite(existing, []string{"new.test"})
	want := base + "# nearhost:start\n127.0.0.1 new.test\n# nearhost:end\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCleanLeavesRestUntouched(t *testing.T) {
	existing := "# before\n" + base + "# nearhost:start\n127.0.0.1 a.test\n# nearhost:end\n# after\n"
	if got, want := Rewrite(existing, nil), "# before\n"+base+"# after\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnsureNoApplyWhenBlockCorrectWithoutTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte("127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 a.test\n# nearhost:end"), 0o644)
	called := false
	changed, err := Ensure(path, []string{"a.test"}, func([]string) error { called = true; return nil })
	if err != nil || changed || called {
		t.Fatalf("changed=%v err=%v called=%v", changed, err, called)
	}
}

func TestSudoCommandCarriesFilePathAndAliases(t *testing.T) {
	cmd := sudoCommand("/bin/nearhost", "/tmp/other-hosts", []string{"a.test", "b.test"})
	args := strings.Join(cmd.Args, " ")
	if !strings.HasSuffix(args, "/bin/nearhost hosts apply --file /tmp/other-hosts") {
		t.Fatalf("args: %s", args)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if string(in) != "a.test\nb.test\n" {
		t.Fatalf("stdin: %q", in)
	}
}
