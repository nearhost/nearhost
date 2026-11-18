package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crlf converts every LF in s to CRLF.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// withDefaultEnding sets the platform line ending for the test.
func withDefaultEnding(t *testing.T, eol string) {
	t.Helper()
	old := defaultLineEnding
	defaultLineEnding = eol
	t.Cleanup(func() { defaultLineEnding = old })
}

const lfBlock = "# nearhost:start\n127.0.0.1 a.test\n::1 a.test\n# nearhost:end\n"

func TestRewriteCRLFFileGetsCRLFBlock(t *testing.T) {
	withDefaultEnding(t, "\n")
	got := mustRewrite(t, crlf(base), []string{"a.test"})
	if want := crlf(base + lfBlock); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteCRLFFileWithLFBlockFrom013IsReplacedOnce(t *testing.T) {
	withDefaultEnding(t, "\n")
	existing := crlf(base) + lfBlock
	got := mustRewrite(t, existing, []string{"a.test"})
	if want := crlf(base + lfBlock); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if n := strings.Count(got, StartMarker); n != 1 {
		t.Fatalf("%d blocks", n)
	}
	if again := mustRewrite(t, got, []string{"a.test"}); again != got {
		t.Fatalf("second rewrite changed it: %q", again)
	}
}

func TestApplyUnchangedCRLFBlockWritesNothing(t *testing.T) {
	withDefaultEnding(t, "\n")
	path := filepath.Join(t.TempDir(), "hosts")
	content := crlf(base + lfBlock)
	writeHosts(t, path, content)
	old := renameFile
	renameFile = func(_, _ string) error { t.Fatal("the file was written"); return nil }
	defer func() { renameFile = old }()
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	if applied, err := Ensure(path, []string{"a.test"}, func([]string) error { t.Fatal("applier called"); return nil }); err != nil || applied {
		t.Fatalf("Ensure = %v, %v", applied, err)
	}
}

func TestApplyRewritesLFBlockInCRLFFileOnce(t *testing.T) {
	withDefaultEnding(t, "\n")
	path := filepath.Join(t.TempDir(), "hosts")
	writeHosts(t, path, crlf(base)+lfBlock)
	if err := Apply(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if want := crlf(base + lfBlock); string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
	renamed := false
	old := renameFile
	renameFile = func(a, b string) error { renamed = true; return old(a, b) }
	defer func() { renameFile = old }()
	if err := Apply(path, []string{"a.test"}); err != nil || renamed {
		t.Fatalf("second Apply wrote (err %v)", err)
	}
}

func TestRewriteLFFileStaysLFOnWindowsDefault(t *testing.T) {
	withDefaultEnding(t, "\r\n")
	got := mustRewrite(t, base, []string{"a.test"})
	if want := base + lfBlock; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteEmptyFileTakesPlatformDefault(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		withDefaultEnding(t, eol)
		got := mustRewrite(t, "", []string{"a.test"})
		if want := strings.ReplaceAll(lfBlock, "\n", eol); got != want {
			t.Fatalf("eol %q: got %q, want %q", eol, got, want)
		}
	}
}

func TestRewriteMostlyLFFileWithSomeCRLFUsesLF(t *testing.T) {
	withDefaultEnding(t, "\r\n")
	existing := "a\r\nb\nc\nd\n"
	got := mustRewrite(t, existing, []string{"a.test"})
	if want := existing + lfBlock; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewritePreservesBytesOutsideBlock(t *testing.T) {
	withDefaultEnding(t, "\n")
	// A BOM, mixed endings, a lone \r, a \r inside a line, trailing blanks and
	// no final newline. CRLF is the majority.
	before := "\xef\xbb\xbf# hosts  \r\n127.0.0.1 localhost \t\r\nmid\rdle\r\nold\rmac\n"
	after := "10.0.0.1 x.test  \r\n10.0.0.2 y.test\t"
	existing := before + "# nearhost:start\n127.0.0.1 old.test\n# nearhost:end\r\n" + after
	got := mustRewrite(t, existing, []string{"a.test"})
	want := before + crlf(lfBlock) + after
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if again := mustRewrite(t, got, []string{"a.test"}); again != got {
		t.Fatalf("second rewrite changed it: %q", again)
	}
}

func TestRewriteAddsNewlineBeforeBlockWithFileEnding(t *testing.T) {
	withDefaultEnding(t, "\n")
	got := mustRewrite(t, "a\r\nb", []string{"a.test"})
	if want := "a\r\nb\r\n" + crlf(lfBlock); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteAddThenRemoveRestoresFile(t *testing.T) {
	for _, original := range []string{crlf(base), base, "a\r\nb\nc\r\n", crlf(base) + "\r\n"} {
		withDefaultEnding(t, "\n")
		added := mustRewrite(t, original, []string{"a.test"})
		removed := mustRewrite(t, added, nil)
		if removed != original {
			t.Fatalf("original %q came back as %q", original, removed)
		}
	}
}

// When Nearhost had to add a newline before its block, removing the block
// leaves that newline in place.
func TestRewriteRemovalLeavesTheNewlineAddedBeforeTheBlock(t *testing.T) {
	withDefaultEnding(t, "\n")
	added := mustRewrite(t, "a\r\nb", []string{"a.test"})
	if got, want := mustRewrite(t, added, nil), "a\r\nb\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := mustRewrite(t, "a\r\nb", nil); got != "a\r\nb" {
		t.Fatalf("a file with no block changed: %q", got)
	}
}

// TestSecondApplyIsANoOp applies each file twice: the first write may change
// it, the second must not write at all.
func TestSecondApplyIsANoOp(t *testing.T) {
	cases := []struct {
		name, eol, content string
	}{
		{"plain CRLF file", "\n", crlf(base)},
		{"empty file, LF default", "\n", ""},
		{"empty file, CRLF default", "\r\n", ""},
		{"block in the middle", "\n", crlf(base) + lfBlock + "10.0.0.1 x.test\r\n"},
		{"block in the middle, no final newline", "\n", crlf(base) + lfBlock + "10.0.0.1 x.test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDefaultEnding(t, tc.eol)
			path := filepath.Join(t.TempDir(), "hosts")
			writeHosts(t, path, tc.content)
			if err := Apply(path, []string{"a.test"}); err != nil {
				t.Fatal(err)
			}
			old := renameFile
			renameFile = func(_, _ string) error { t.Fatal("the second Apply wrote"); return nil }
			defer func() { renameFile = old }()
			if err := Apply(path, []string{"a.test"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// writeHosts writes content to path.
func writeHosts(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAliasesReadsCRLFBlock(t *testing.T) {
	got := Aliases(crlf(base + lfBlock))
	if len(got) != 1 || got[0] != "a.test" {
		t.Fatalf("got %v", got)
	}
}
