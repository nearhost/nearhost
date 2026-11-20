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
	"sync/atomic"
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
	// macOS and Windows may spell the path in another case.
	if !strings.EqualFold(slashed(runtime.GOOS, got), p) {
		t.Errorf("openedPath = %q, want %q", got, p)
	}
}

func TestSameFileAt(t *testing.T) {
	dir, p := openTree(t)
	open := func() *os.File {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// Each file is closed once checked: Windows refuses to rename a folder
	// that holds an open file, which would stop the next swap.
	f := open()
	if !sameFileAt(f, p) {
		t.Error("an unchanged file is not the one at its path")
	}
	f.Close()
	// Swapped before the open, and still swapped.
	swapNotes(t, dir)
	f = open()
	if sameFileAt(f, p) {
		t.Error("a file opened through a swapped folder passes")
	}
	f.Close()
	// Swapped before the open, and swapped back before the check. The file
	// is open in dir/outside, so notes can be swapped back with it open.
	f = open()
	defer f.Close()
	unswapNotes(t, dir)
	if sameFileAt(f, p) {
		t.Error("a file opened through a folder swapped back passes")
	}
}

func TestSameRootHandle(t *testing.T) {
	for _, tc := range []struct {
		goos, current, stored string
		want                  bool
	}{
		{"linux", "/home/u/p", "/home/u/p", true},
		{"linux", "/home/u/b/p", "/home/u/p", false},
		{"linux", "/home/u/P", "/home/u/p", false},
		{"linux", "", "", false},
		// A respelled root: the same call gave both, so they agree.
		{"darwin", "/System/Volumes/Data/Users/u/p", "/System/Volumes/Data/Users/u/p", true},
		{"darwin", "/Users/Jose\u0301/p", "/Users/Jose\u0301/p", true},
		{"windows", `\\?\C:\real\p`, `\\?\C:\real\p`, true},
		{"windows", `\\?\UNC\srv\share\p`, `\\?\UNC\srv\share\p`, true},
		{"windows", `\\?\C:\Users\U\P`, `\\?\C:\Users\u\p`, true},
		// A root that is now elsewhere, as through a swapped folder
		// above it: refused, with nothing to fall back to.
		{"darwin", "/Users/u/b/p", "/Users/u/p", false},
		{"darwin", "/Users/u/P", "/Users/u/p", false},
		{"darwin", "/Users/Jos\u00e9/p", "/Users/Jose\u0301/p", false},
		{"windows", `\\?\C:\Users\u\b\p`, `\\?\C:\Users\u\p`, false},
		{"windows", `\\?\S:\p`, `\\?\C:\real\p`, false},
		{"windows", "", `\\?\C:\real\p`, false},
	} {
		if got := sameRootHandle(tc.goos, tc.current, tc.stored); got != tc.want {
			t.Errorf("sameRootHandle(%s, %q, %q) = %v, want %v", tc.goos, tc.current, tc.stored, got, tc.want)
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
		{"linux", "/home/u/p", "/home/u/p/a.md (deleted)", "", false},
		{"darwin", "/Users/u/p", "/Users/u/p/a.md (deleted)", "a.md (deleted)", true},
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
	r, rel, err := LocalFS{}.Open(context.Background(), dir, rootHandlePath(dir), p)
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
	handle := rootHandlePath(dir + "/notes")
	swapNotes(t, dir)
	if r, rel, err := (LocalFS{}).Open(context.Background(), dir+"/notes", handle, p); !errors.Is(err, fs.ErrNotExist) {
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
	if r, _, err := fsys.Open(context.Background(), dir, "", p); !errors.Is(err, fs.ErrNotExist) {
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
	r, _, err := fsys.Open(context.Background(), dir, "", p)
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
	if r, _, err := fsys.Open(context.Background(), dir, "", p); !errors.Is(err, fs.ErrNotExist) {
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

func TestTreeBindsItsRootHandle(t *testing.T) {
	f := newFixture(t)
	want := ""
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		want = rootHandlePath(f.root)
		if want == "" {
			t.Fatal("rootHandlePath gave nothing for the fixture's root")
		}
	}
	if f.tree.RootHandle != want {
		t.Errorf("RootHandle = %q, want %q", f.tree.RootHandle, want)
	}
	if tr := f.table.Add("apollo", f.root, "claude", "plan.md"); tr.RootHandle != "" {
		t.Errorf("a tree on a server has RootHandle %q, want none", tr.RootHandle)
	}
}

func TestFolderAboveRootSwappedServesNothing(t *testing.T) {
	// The tree's root is dir/a/root. dir/a is swapped for a link to dir/b,
	// which holds a root/ of its own, after the checks: the root folder
	// opened then isn't the one the tree was made with.
	f := newFixtureAt(t, "a/root")
	f.write(t, "notes/x.md", "# X")
	b := filepath.Join(f.dir, "b", "root", "notes")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "x.md"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(f.dir, "a")
	beforeOpen = func(string) {
		if err := os.Rename(a, a+"-old"); err != nil {
			t.Error(err)
			return
		}
		testutil.Symlink(t, filepath.Join(f.dir, "b"), a)
	}
	t.Cleanup(func() { beforeOpen = nil })
	rec := f.do(t, "GET", "notes/x.md?raw")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "SECRET") || !strings.Contains(rec.Body.String(), "isn't there any more") {
		t.Errorf("folder above the root swapped: %d %q", rec.Code, rec.Body.String())
	}
}

func TestUnreadableRootStillServes(t *testing.T) {
	// A root the laptop may search but not list can't be opened when the
	// tree is made, so the tree has no RootHandle and each file is checked
	// by its identity instead.
	if runtime.GOOS == "windows" {
		t.Skip("folder permission bits are Unix only")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	dir, err := LocalFS{}.RealPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := dir + "/root"
	if err := os.MkdirAll(root+"/notes", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/notes/a.md", []byte("# A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })
	tbl := NewTable(time.Now)
	tr := tbl.Add(LocalServer, root, "claude", "notes/a.md")
	if tr.RootHandle != "" {
		t.Fatalf("RootHandle of an unreadable root = %q, want none", tr.RootHandle)
	}
	h := NewHandler(tbl, func(context.Context, string) (FS, error) { return LocalFS{}, nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d/"+tr.ID+"/notes/a.md?raw", nil))
	if rec.Code != 200 || rec.Body.String() != "# A" {
		t.Errorf("a file below an unreadable root: %d %q", rec.Code, rec.Body.String())
	}
}

func TestFileDeletedAfterOpenIsMissing(t *testing.T) {
	// /proc names a file deleted since it was opened "… (deleted)".
	if runtime.GOOS != "linux" {
		t.Skip("only Linux names a deleted file this way")
	}
	dir, p := openTree(t)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := openedBelowRoot(f, dir, rootHandlePath(dir), p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file deleted after the open: err = %v, want fs.ErrNotExist", err)
	}
}

// touchFolderDuringChecks sets sftpCheckHook to give dir/notes a new
// modification time in the middle of each of the first n checks, as a write
// to the folder does, and returns the number of checks begun. The waits
// between tries are cut short for the test.
func touchFolderDuringChecks(t *testing.T, dir string, n int32) *atomic.Int32 {
	t.Helper()
	pause := openPause
	openPause = time.Microsecond
	t.Cleanup(func() { openPause = pause })
	checks := new(atomic.Int32)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	setSFTPCheckHook(t, func(stage string) {
		switch {
		case stage == "walk":
			checks.Add(1)
		case checks.Load() <= n:
			at = at.Add(time.Minute)
			if err := os.Chtimes(filepath.Join(dir, "notes"), at, at); err != nil {
				t.Error(err)
			}
		}
	})
	return checks
}

func TestSFTPFolderWrittenDuringChecksIsServedOnceItSettles(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	checks := touchFolderDuringChecks(t, dir, openTries-1)
	r, rel, err := fsys.Open(context.Background(), dir, "", p)
	if err != nil {
		t.Fatalf("a folder written to during %d checks: %v", openTries-1, err)
	}
	defer r.Close()
	if b, err := io.ReadAll(r); err != nil || string(b) != "notes/a.md" || rel != "notes/a.md" {
		t.Errorf("read %q as %q, %v", b, rel, err)
	}
	if n := checks.Load(); n != openTries {
		t.Errorf("%d checks, want %d", n, openTries)
	}
}

func TestSFTPFolderWrittenDuringEveryCheckIsChanging(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	checks := touchFolderDuringChecks(t, dir, openTries)
	r, _, err := fsys.Open(context.Background(), dir, "", p)
	if r != nil {
		r.Close()
	}
	if !errors.Is(err, ErrChanging) {
		t.Fatalf("err = %v, want ErrChanging", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Error("ErrChanging reads as a missing file")
	}
	if n := checks.Load(); n != openTries {
		t.Errorf("%d checks, want %d", n, openTries)
	}
	if brokenClient(context.Background(), err) {
		t.Error("a folder that keeps changing dropped the pooled client")
	}
}

// A swap is refused at the first check: only a change in size or time is
// tried again.
func TestSFTPSwappedPathIsCheckedOnce(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	swapNotes(t, dir)
	checks := 0
	setSFTPCheckHook(t, func(stage string) {
		if stage == "walk" {
			checks++
		}
	})
	r, _, err := fsys.Open(context.Background(), dir, "", p)
	if r != nil {
		r.Close()
	}
	if !errors.Is(err, fs.ErrNotExist) || checks != 1 {
		t.Errorf("err = %v after %d checks, want fs.ErrNotExist after 1", err, checks)
	}
}

func TestSFTPOpenStopsTryingWhenTheRequestEnds(t *testing.T) {
	dir, p := openTree(t)
	fsys := volumeFS{SFTPFS(inProcessSFTP(t, dir))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The request ends in the middle of the first check, which also sees
	// the folder change.
	var checks atomic.Int32
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	setSFTPCheckHook(t, func(stage string) {
		if stage == "walk" {
			checks.Add(1)
			return
		}
		if err := os.Chtimes(filepath.Join(dir, "notes"), at, at); err != nil {
			t.Error(err)
		}
		cancel()
	})
	r, _, err := fsys.Open(ctx, dir, "", p)
	if r != nil {
		r.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the request's cancellation", err)
	}
	// Open returns at the cancel; the check it was in ends just after.
	time.Sleep(50 * time.Millisecond)
	if n := checks.Load(); n != 1 {
		t.Errorf("%d checks, want 1", n)
	}
}

func TestChangingPage(t *testing.T) {
	tbl := NewTable(time.Now)
	tr := tbl.Add("apollo", "/srv/app", "claude", "plan.md")
	h := NewHandler(tbl, func(context.Context, string) (FS, error) {
		return fakeFS{err: &fs.PathError{Op: "open", Path: "/srv/app/plan.md", Err: ErrChanging}}, nil
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/"+tr.ID+"/plan.md", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	for _, want := range []string{"folder keeps changing", "/srv/app/plan.md", "apollo", "Reload this page"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page lacks %q:\n%s", want, rec.Body.String())
		}
	}
	checkHeaders(t, rec, mdCSP)
}
