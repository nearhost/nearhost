package discovery

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirReaderRefusesALargeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), bytes.Repeat([]byte("#"), 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DirReader{Root: dir}.ReadFile(".env")
	if err == nil || err.Error() != ".env is larger than 1 MiB. Nearhost reads project files up to that size; make it smaller or remove it." {
		t.Fatalf("err = %v", err)
	}
}

func TestDirReaderReadsAFileOfTheLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), bytes.Repeat([]byte("#"), MaxProjectFile), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := DirReader{Root: dir}.ReadFile(".env")
	if err != nil || len(data) != MaxProjectFile {
		t.Fatalf("read %d bytes, err %v", len(data), err)
	}
}

// endless is a reader that never runs out, standing in for a file that
// grows while it is read. It stops after a bound so a broken limit fails
// the test rather than filling memory.
type endless struct{ n int }

func (e *endless) Read(p []byte) (int, error) {
	if e.n >= 4*MaxProjectFile {
		return 0, errors.New("read past the limit")
	}
	e.n += len(p)
	return len(p), nil
}

func TestReadLimitedStopsAFileThatKeepsGoing(t *testing.T) {
	r := &endless{}
	_, err := ReadLimited(".env", r)
	if err == nil || err.Error() != ".env is larger than 1 MiB. Nearhost reads project files up to that size; make it smaller or remove it." {
		t.Fatalf("err = %v", err)
	}
	if r.n > MaxProjectFile+64<<10 {
		t.Errorf("read %d bytes", r.n)
	}
}

func TestReadLimitedReadsTheWholeOfASmallFile(t *testing.T) {
	data, err := ReadLimited(".env", io.MultiReader(strings.NewReader("A=1\n"), strings.NewReader("B=2\n")))
	if err != nil || string(data) != "A=1\nB=2\n" {
		t.Fatalf("ReadLimited = %q %v", data, err)
	}
}

func TestDirReaderReportsADirectoryAsAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := DirReader{Root: dir}.ReadFile(".env")
	if err == nil || !isAbsent(err) {
		t.Fatalf("err = %v, want one isAbsent counts as no file", err)
	}
}

func TestDirReaderExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big"), bytes.Repeat([]byte("#"), 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := DirReader{Root: dir}
	for name, want := range map[string]bool{"big": true, "vendor": false, "missing": false, "big/under": false} {
		got, err := r.Exists(name)
		if err != nil || got != want {
			t.Errorf("Exists(%q) = %v %v, want %v", name, got, err, want)
		}
	}
}

func TestMemoExistsFailureFailsTheRun(t *testing.T) {
	m := newMemo(existsFails{})
	if _, err := m.Exists("a"); err == nil {
		t.Fatal("expected the error")
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "read a: denied") {
		t.Errorf("m.err = %v", m.err)
	}
}

// existsFails fails every Exists.
type existsFails struct{ memReader }

func (existsFails) Exists(string) (bool, error) { return false, errors.New("denied") }

// A refused file is named once wherever its error is passed on: by ReadEnv,
// ReadCompose and the memo a preset reads through.
func TestARefusedFileIsNamedOnce(t *testing.T) {
	big := bytes.Repeat([]byte("#"), 2<<20)
	for _, name := range []string{".env", "compose.yaml"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), big, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Discover(DirReader{Root: dir}, Options{})
		if err == nil || err.Error() != name+" is larger than 1 MiB. Nearhost reads project files up to that size; make it smaller or remove it." {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	m := newMemo(DirReader{Root: t.TempDir()})
	m.fail("vite.config.js", tooLarge("vite.config.js"))
	if m.err == nil || m.err.Error() != "vite.config.js is larger than 1 MiB. Nearhost reads project files up to that size; make it smaller or remove it." {
		t.Errorf("memo: err = %v", m.err)
	}
}

func TestReadErrorNamesOtherErrors(t *testing.T) {
	if got := ReadError(".env", errors.New("permission denied")).Error(); got != "read .env: permission denied" {
		t.Errorf("got %q", got)
	}
	if got := ReadError(".env.local", tooLarge(".env")).Error(); got != "read .env.local: .env is larger than 1 MiB. Nearhost reads project files up to that size; make it smaller or remove it." {
		t.Errorf("got %q", got)
	}
}
