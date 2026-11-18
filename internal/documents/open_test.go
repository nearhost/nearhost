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
	"time"

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
	// macOS and Windows may spell the path another way; the folder's
	// identity decides then.
	if m := sameSpelling(runtime.GOOS, got, p); m == matchDiffers || !strings.EqualFold(slashed(runtime.GOOS, got), p) {
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

func TestSameSpelling(t *testing.T) {
	for _, tc := range []struct {
		goos, opened, checked string
		want                  openedMatch
	}{
		{"linux", "/home/u/p", "/home/u/p", matchSame},
		{"linux", "/home/u/.ssh", "/home/u/p", matchDiffers},
		{"linux", "/home/u/P", "/home/u/p", matchDiffers},
		{"darwin", "/Users/u/p", "/Users/u/p", matchSame},
		{"darwin", "/System/Volumes/Data/Users/u/p", "/Users/u/p", matchUnsure},
		{"darwin", "/Users/Jose\u0301/p", "/Users/Jos\u00e9/p", matchUnsure},
		{"windows", `\\?\C:\Users\u\p`, "C:/Users/u/p", matchSame},
		{"windows", `\\?\C:\Users\U\P`, "C:/Users/u/p", matchSame},
		{"windows", `\\?\UNC\srv\share\p`, "//srv/share/p", matchSame},
		{"windows", `\\?\C:\real\p`, "S:/p", matchUnsure},
	} {
		if got := sameSpelling(tc.goos, tc.opened, tc.checked); got != tc.want {
			t.Errorf("sameSpelling(%s, %q, %q) = %v, want %v", tc.goos, tc.opened, tc.checked, got, tc.want)
		}
	}
}

func TestOpenedBelow(t *testing.T) {
	for _, tc := range []struct {
		goos, root, file, want string
		ok                     bool
	}{
		{"linux", "/home/u/p", "/home/u/p/notes/a.md", "notes/a.md", true},
		{"linux", "/home/u/p", "/home/u/outside/a.md", "", false},
		{"linux", "/home/u/p", "/home/u/p2/a.md", "", false},
		{"linux", "/home/u/p", "/home/u/p/.git/a.md", "", false},
		{"linux", "/home/u/p", "/home/u/P/a.md", "", false},
		{"linux", "/home/u/p", "/home/u/p/a.md (deleted)", "a.md (deleted)", true},
		{"linux", "/home/u/p", "/home/u/p", "", false},
		// A folder swapped for a link: the file opened is outside the
		// root, and nothing falls back.
		{"darwin", "/Users/u/p", "/Users/u/.ssh/id_ed25519", "", false},
		{"darwin", "/Users/u/p", "/Users/u/outside/a.md", "", false},
		{"windows", `\\?\C:\Users\u\p`, `\\?\C:\Users\u\.ssh\a.md`, "", false},
		{"windows", `\\?\C:\Users\u\p`, `\\?\C:\Users\u\outside\a.md`, "", false},
		// A subst drive or firmlink: root and file share the new prefix.
		{"windows", `\\?\C:\real\p`, `\\?\C:\real\p\notes\a.md`, "notes/a.md", true},
		{"windows", `\\?\UNC\srv\share\p`, `\\?\UNC\srv\share\p\a.md`, "a.md", true},
		{"darwin", "/System/Volumes/Data/Users/u/p", "/System/Volumes/Data/Users/u/p/a.md", "a.md", true},
		// Case: Windows ignores it, macOS compares its own two paths exactly.
		{"windows", `\\?\C:\Users\u\Proj`, `\\?\C:\USERS\U\proj\Notes\a.md`, "Notes/a.md", true},
		{"darwin", "/Users/u/Proj", "/Users/u/proj/a.md", "", false},
		// Non-ASCII: served when root and file agree, refused outside.
		{"darwin", "/Users/Jose\u0301/p", "/Users/Jose\u0301/p/caf\u00e9.md", "caf\u00e9.md", true},
		{"darwin", "/Users/Jose\u0301/p", "/Users/Jose\u0301/.ssh/id", "", false},
		{"windows", `\\?\C:\Users\José\p`, `\\?\C:\Users\José\p\a.md`, "a.md", true},
		{"windows", `\\?\C:\Users\José\p`, `\\?\C:\Users\José\.ssh\a.md`, "", false},
		{"windows", `\\?\C:\`, `\\?\C:\a.md`, "a.md", true},
	} {
		got, ok := openedBelow(tc.goos, tc.root, tc.file)
		if got != tc.want || ok != tc.ok {
			t.Errorf("openedBelow(%s, %q, %q) = %q, %v; want %q, %v", tc.goos, tc.root, tc.file, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLocalOpenGivesThePathBelowTheRoot(t *testing.T) {
	dir, p := openTree(t)
	r, rel, err := LocalFS{}.Open(context.Background(), dir, p)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if rel != "notes/a.md" {
		t.Errorf("Open's path below the root = %q, want notes/a.md", rel)
	}
}

func TestLocalOpenRefusesASwappedRoot(t *testing.T) {
	// The root, dir/notes, swapped for a link after it was checked. A
	// folder below the root swapped is TestSwapBetweenCheckAndOpenIsMissing.
	dir, p := openTree(t)
	swapNotes(t, dir)
	if r, rel, err := (LocalFS{}).Open(context.Background(), dir+"/notes", p); !errors.Is(err, fs.ErrNotExist) {
		if r != nil {
			b, _ := io.ReadAll(r)
			r.Close()
			t.Errorf("Open through a swapped folder read %q as %q, %v; want fs.ErrNotExist", b, rel, err)
			return
		}
		t.Errorf("Open through a swapped folder err = %v, want fs.ErrNotExist", err)
	}
}

func TestSFTPOpenRefusesASwappedPath(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	swapNotes(t, dir)
	if r, _, err := fsys.Open(context.Background(), dir, p); !errors.Is(err, fs.ErrNotExist) {
		if r != nil {
			r.Close()
		}
		t.Errorf("Open over SFTP through a swapped folder err = %v, want fs.ErrNotExist", err)
	}
}

// setSFTPCheckHook sets sftpCheckHook for the test.
func setSFTPCheckHook(t *testing.T, hook func(stage string)) {
	t.Helper()
	sftpCheckHook = hook
	t.Cleanup(func() { sftpCheckHook = nil })
}

func TestSFTPFileGrowingWhileCheckedIsServed(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	grown := false
	setSFTPCheckHook(t, func(stage string) {
		if stage != "fstat" || grown {
			return
		}
		grown = true
		g, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer g.Close()
		if _, err := g.WriteString(" and more"); err != nil {
			t.Error(err)
		}
	})
	r, _, err := fsys.Open(context.Background(), dir, p)
	if err != nil {
		t.Fatalf("a file written to while it was checked: %v", err)
	}
	defer r.Close()
	if b, err := io.ReadAll(r); err != nil || string(b) != "notes/a.md and more" {
		t.Errorf("read %q, %v", b, err)
	}
}

func TestSFTPFolderSwappedDuringCheckIsMissing(t *testing.T) {
	dir, p := openTree(t)
	// The file in the tree and the one outside look alike to SFTP, so only
	// the second look at the folders can tell.
	same := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, name := range []string{p, filepath.Join(dir, "outside", "a.md")} {
		if err := os.WriteFile(name, []byte("SIXBYT"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, same, same); err != nil {
			t.Fatal(err)
		}
	}
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	// Swapped for the open, back for the walk, swapped again after it.
	swapNotes(t, dir)
	setSFTPCheckHook(t, func(stage string) {
		switch stage {
		case "walk":
			unswapNotes(t, dir)
		case "fstat":
			swapNotes(t, dir)
		}
	})
	if r, _, err := fsys.Open(context.Background(), dir, p); !errors.Is(err, fs.ErrNotExist) {
		if r != nil {
			r.Close()
		}
		t.Errorf("Open over SFTP with a folder swapped during the check err = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenedFileIsJudgedByItsOwnName(t *testing.T) {
	// The last element swapped for a link inside the tree after the
	// checks: the rules apply to the file opened.
	t.Run("hidden", func(t *testing.T) {
		f := newFixtureAt(t, ".ssh")
		f.write(t, "a.md", "# A")
		f.write(t, "id_ed25519", "SECRET")
		a := filepath.Join(f.root, "a.md")
		beforeOpen = func(string) {
			if err := os.Remove(a); err != nil {
				t.Error(err)
				return
			}
			testutil.Symlink(t, "id_ed25519", a)
		}
		t.Cleanup(func() { beforeOpen = nil })
		if rec := f.do(t, "GET", "a.md?raw"); rec.Code != 404 || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("a.md swapped for a link to id_ed25519: %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("asset", func(t *testing.T) {
		f := newFixture(t)
		f.write(t, "app.js", "run()")
		f.write(t, "secrets.json", `{"k":1}`)
		js := filepath.Join(f.root, "app.js")
		beforeOpen = func(string) {
			if err := os.Remove(js); err != nil {
				t.Error(err)
				return
			}
			testutil.Symlink(t, "secrets.json", js)
		}
		t.Cleanup(func() { beforeOpen = nil })
		rec := f.do(t, "GET", "app.js")
		if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "javascript") {
			t.Errorf("app.js swapped for a link to secrets.json served as %q", ct)
		}
		if _, ok := rec.Header()["Access-Control-Allow-Origin"]; ok {
			t.Error("app.js swapped for a link to secrets.json got Access-Control-Allow-Origin")
		}
	})
}
