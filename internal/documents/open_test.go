package documents

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

// swapBeforeOpen sets beforeOpen, for the test, to swap f's root/notes for a
// symlink to dir/outside once the served path has been checked, as an agent
// that writes in the tree could between the check and the open.
func swapBeforeOpen(t *testing.T, f *fixture) {
	t.Helper()
	outside := filepath.Join(f.dir, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.md"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(f.root, "notes")
	beforeOpen = func(string) {
		if err := os.Rename(notes, notes+"-old"); err != nil {
			t.Error(err)
			return
		}
		testutil.Symlink(t, outside, notes)
	}
	t.Cleanup(func() { beforeOpen = nil })
}

func TestSwapBetweenCheckAndOpenIsMissing(t *testing.T) {
	f := newFixture(t)
	f.write(t, "notes/a.md", "# A")
	swapBeforeOpen(t, f)
	rec := f.do(t, "GET", "notes/a.md?raw")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "SECRET") || !strings.Contains(rec.Body.String(), "isn't there any more") {
		t.Errorf("swapped before the open: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSwapBetweenCheckAndOpenOverSFTPIsMissing(t *testing.T) {
	f := newFixture(t)
	f.write(t, "notes/a.md", "# A")
	client := inProcessSFTP(t, f.dir)
	remote := NewHandler(f.table, func(context.Context, string) (FS, error) { return volumeFS{SFTPFS(client)}, nil })
	swapBeforeOpen(t, f)
	rec := httptest.NewRecorder()
	remote.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+f.tree.ID+"/notes/a.md?raw", nil))
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "SECRET") || !strings.Contains(rec.Body.String(), "isn't there any more") {
		t.Errorf("swapped before the open over SFTP: %d %q", rec.Code, rec.Body.String())
	}
}

// openTree makes dir/notes/a.md and dir/outside/a.md and returns the
// resolved dir and the path of notes/a.md.
func openTree(t *testing.T) (dir, p string) {
	t.Helper()
	dir, err := LocalFS{}.RealPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"notes/a.md", "outside/a.md"} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, dir + "/notes/a.md"
}

// swapNotes puts a symlink to dir/outside in place of dir/notes.
func swapNotes(t *testing.T, dir string) {
	t.Helper()
	notes := filepath.Join(dir, "notes")
	if err := os.Rename(notes, notes+"-old"); err != nil {
		t.Fatal(err)
	}
	testutil.Symlink(t, filepath.Join(dir, "outside"), notes)
}

// unswapNotes undoes swapNotes.
func unswapNotes(t *testing.T, dir string) {
	t.Helper()
	notes := filepath.Join(dir, "notes")
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(notes+"-old", notes); err != nil {
		t.Fatal(err)
	}
}

func TestOpenedPathNamesTheFile(t *testing.T) {
	_, p := openTree(t)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := openedPath(f)
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("openedPath on %s = %q, %v; want ErrUnsupported", runtime.GOOS, got, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// macOS and Windows may spell the path in another case; the check
	// falls back to the file's identity then.
	slashed := strings.ReplaceAll(cleanFinalPath(got), `\`, "/")
	if m := compareOpened(runtime.GOOS, got, p); m == matchDiffers || !strings.EqualFold(slashed, p) {
		t.Errorf("openedPath = %q (%v), want %q", got, m, p)
	}
}

func TestSameFileAt(t *testing.T) {
	dir, p := openTree(t)
	open := func() *os.File {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	if f := open(); !sameFileAt(f, p) {
		t.Error("an unchanged file is not the one at its path")
	}
	// Swapped before the open, and still swapped.
	swapNotes(t, dir)
	if f := open(); sameFileAt(f, p) {
		t.Error("a file opened through a swapped folder passes")
	}
	// Swapped before the open, and swapped back before the check.
	f := open()
	unswapNotes(t, dir)
	if sameFileAt(f, p) {
		t.Error("a file opened through a folder swapped back passes")
	}
}

func TestCompareOpened(t *testing.T) {
	for _, tc := range []struct {
		goos, opened, checked string
		want                  openedMatch
	}{
		{"linux", "/home/u/p/a.md", "/home/u/p/a.md", matchSame},
		{"linux", "/home/u/.ssh/a.md", "/home/u/p/a.md", matchDiffers},
		{"linux", "/home/u/P/a.md", "/home/u/p/a.md", matchDiffers},
		{"linux", "/home/u/p/a.md (deleted)", "/home/u/p/a.md", matchDiffers},
		{"darwin", "/Users/u/p/a.md", "/Users/u/p/a.md", matchSame},
		{"darwin", "/Users/u/p/README.md", "/Users/u/p/readme.md", matchUnsure},
		{"darwin", "/Users/u/p/cafe\u0301.md", "/Users/u/p/caf\u00e9.md", matchUnsure},
		{"darwin", "/Users/u/.ssh/a.md", "/Users/u/p/a.md", matchDiffers},
		{"windows", `\\?\C:\Users\u\p\a.md`, "C:/Users/u/p/a.md", matchSame},
		{"windows", `\\?\UNC\srv\share\p\a.md`, "//srv/share/p/a.md", matchSame},
		{"windows", `\\?\C:\Users\u\.ssh\a.md`, "C:/Users/u/p/a.md", matchUnsure},
		{"windows", `\\?\C:\real\p\a.md`, "S:/p/a.md", matchUnsure},
	} {
		if got := compareOpened(tc.goos, tc.opened, tc.checked); got != tc.want {
			t.Errorf("compareOpened(%s, %q, %q) = %v, want %v", tc.goos, tc.opened, tc.checked, got, tc.want)
		}
	}
}

func TestLocalOpenRefusesASwappedPath(t *testing.T) {
	dir, p := openTree(t)
	swapNotes(t, dir)
	if r, err := (LocalFS{}).Open(context.Background(), p); !errors.Is(err, fs.ErrNotExist) {
		if r != nil {
			b, _ := io.ReadAll(r)
			r.Close()
			t.Errorf("Open through a swapped folder read %q, %v; want fs.ErrNotExist", b, err)
			return
		}
		t.Errorf("Open through a swapped folder err = %v, want fs.ErrNotExist", err)
	}
}

func TestSFTPOpenRefusesASwappedPath(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	swapNotes(t, dir)
	if r, err := fsys.Open(context.Background(), p); !errors.Is(err, fs.ErrNotExist) {
		if r != nil {
			r.Close()
		}
		t.Errorf("Open over SFTP through a swapped folder err = %v, want fs.ErrNotExist", err)
	}
}
