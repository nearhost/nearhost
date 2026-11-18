package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// A config kept as a link into a dotfiles repo is written through: the link
// stays a link and the file it points at holds the new config.
func TestSaveWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	dotfiles := filepath.Join(dir, "dotfiles")
	if err := os.Mkdir(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dotfiles, "nearhost.yaml")
	if err := os.WriteFile(real, []byte("servers: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config", "config.yaml")
	if err := os.Mkdir(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.Symlink(t, real, link)
	if err := Save(link, sample()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	data, err := os.ReadFile(real)
	if err != nil || !strings.Contains(string(data), "myapp.test") {
		t.Fatalf("linked file = %q, %v", data, err)
	}
	// No temp file is left beside the link or the file.
	for _, d := range []string{dotfiles, filepath.Dir(link)} {
		entries, _ := os.ReadDir(d)
		if len(entries) != 1 {
			t.Errorf("%s holds %d entries, want 1", d, len(entries))
		}
	}
}

// A link to a file that does not exist is an error: writing a new file over
// it would cut the config off from the file the user manages.
func TestSaveRefusesADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.yaml")
	testutil.Symlink(t, filepath.Join(dir, "gone.yaml"), link)
	err := Save(link, sample())
	if err == nil {
		t.Fatal("Save wrote through a link to a missing file")
	}
	if !strings.Contains(err.Error(), link) || !strings.Contains(err.Error(), "gone.yaml") {
		t.Errorf("error %q should name the link and its target", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Save created the link's target: %v", err)
	}
}

// A user's chmod 600 survives a save, and a new config is 0644 in a 0700
// directory.
func TestSaveKeepsTheFileMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nearhost")
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, sample()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	testutil.WantPerm(t, dir, 0o700)
	testutil.WantPerm(t, path, 0o644)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, sample()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	testutil.WantPerm(t, path, 0o600)
}

// The lock that Update takes creates the config directory 0700 too.
func TestUpdateCreatesThePrivateConfigDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nearhost")
	if err := Update(filepath.Join(dir, "config.yaml"), func(*Config) error { return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	testutil.WantPerm(t, dir, 0o700)
}

// An existing empty config is an error that says what to do, so a crash that
// left it empty never reads as "no projects" and gets written over.
func TestLoadRefusesAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	want := path + " is empty. Restore it from a backup, or delete it to start afresh."
	if err == nil || err.Error() != want {
		t.Fatalf("Load error = %v, want %q", err, want)
	}
	if err := Update(path, func(*Config) error { return nil }); err == nil {
		t.Fatal("Update wrote over an empty config")
	}
	if data, _ := os.ReadFile(path); len(data) != 0 {
		t.Fatalf("Update changed the empty file: %q", data)
	}
}

// WriteFileAtomic gives a new file the mode it is asked for and leaves no
// temp file behind.
func TestWriteFileAtomicCreatesAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.yaml")
	if err := WriteFileAtomic(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "new\n" {
		t.Fatalf("file = %q, %v", data, err)
	}
	testutil.WantPerm(t, path, 0o600)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want 1", len(entries))
	}
}

// errBusy stands in for a rename Windows refuses because another program
// holds the file open: a sharing violation or access denied.
var errBusy = errors.New("busy")

// A rename that fails twice because the file is held open is tried again, 50 ms
// apart, and succeeds on the third try.
func TestRetryRenameRetriesWhileBusy(t *testing.T) {
	calls := 0
	var slept []time.Duration
	rename := retryRename(func(string, string) error {
		calls++
		if calls <= 2 {
			return errBusy
		}
		return nil
	}, func(err error) bool { return errors.Is(err, errBusy) }, func(d time.Duration) { slept = append(slept, d) })
	if err := rename("a", "b"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if calls != 3 {
		t.Errorf("rename tried %d times, want 3", calls)
	}
	if len(slept) != 2 || slept[0] != 50*time.Millisecond || slept[1] != 50*time.Millisecond {
		t.Errorf("slept %v, want two waits of 50ms", slept)
	}
}

// A file held open for longer gives up after 500 ms of waiting and
// returns the error.
func TestRetryRenameGivesUp(t *testing.T) {
	calls := 0
	var waited time.Duration
	rename := retryRename(func(string, string) error {
		calls++
		return errBusy
	}, func(err error) bool { return errors.Is(err, errBusy) }, func(d time.Duration) { waited += d })
	if err := rename("a", "b"); !errors.Is(err, errBusy) {
		t.Fatalf("rename error = %v, want errBusy", err)
	}
	if waited != 500*time.Millisecond || calls != 11 {
		t.Errorf("waited %v over %d tries, want 500ms over 11", waited, calls)
	}
}

// Any other error returns at once.
func TestRetryRenameReturnsOtherErrors(t *testing.T) {
	calls := 0
	other := errors.New("other")
	rename := retryRename(func(string, string) error {
		calls++
		return other
	}, func(err error) bool { return errors.Is(err, errBusy) }, func(time.Duration) { t.Error("slept on an error that is not busy") })
	if err := rename("a", "b"); !errors.Is(err, other) || calls != 1 {
		t.Fatalf("rename = %v after %d tries", err, calls)
	}
}

// A file kept as a relative link, such as a preset the user manages, is
// written through: the link stays and its file gets the new contents and
// keeps its own mode.
func TestWriteFileAtomicWritesThroughARelativeLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "mine.yaml")
	if err := os.WriteFile(real, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "preset.yaml")
	testutil.Symlink(t, "mine.yaml", link)
	if err := WriteFileAtomic(link, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	if data, err := os.ReadFile(real); err != nil || string(data) != "new\n" {
		t.Fatalf("linked file = %q, %v", data, err)
	}
	testutil.WantPerm(t, real, 0o600)
}

// A rename that is still busy when the retries run out says which file is
// open and what to do, and leaves no temp file.
func TestWriteFileAtomicNamesAFileHeldOpen(t *testing.T) {
	saved := renameIntoPlace
	t.Cleanup(func() { renameIntoPlace = saved })
	renameIntoPlace = retryRename(func(string, string) error { return errBusy },
		func(err error) bool { return errors.Is(err, errBusy) }, func(time.Duration) {})
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := WriteFileAtomic(path, []byte("new\n"), 0o644)
	want := path + " is open in another program. Close it and try again."
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("dir holds %d entries, want 0", len(entries))
	}
}

// A rename still refused when the retries run out over a read-only file
// says the file is read-only, which is what the user can fix; Windows
// refuses such a rename with access denied, as it does a file held open.
func TestWriteFileAtomicNamesAReadOnlyFile(t *testing.T) {
	saved := renameIntoPlace
	t.Cleanup(func() { renameIntoPlace = saved })
	renameIntoPlace = retryRename(func(string, string) error { return errBusy },
		func(err error) bool { return errors.Is(err, errBusy) }, func(time.Duration) {})
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("old\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	err := WriteFileAtomic(path, []byte("new\n"), 0o644)
	// The message names the file by its resolved path, which differs from
	// the temp folder's on macOS and on Windows runners.
	resolved, rerr := filepath.EvalSymlinks(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	want := resolved + " is read-only, so Nearhost cannot save it. Clear the file's read-only attribute and try again."
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want only the file", len(entries))
	}
}

// stillBusy blames another program for a sharing violation, and for access
// denied over a file that is not read-only; access denied over a read-only
// file is reported as such.
func TestStillBusyNamesTheCause(t *testing.T) {
	const path = "config.yaml"
	held := path + " is open in another program. Close it and try again."
	readOnly := path + " is read-only, so Nearhost cannot save it. Clear the file's read-only attribute and try again."
	for _, tc := range []struct {
		sharing, readOnly bool
		want              string
	}{
		{sharing: true, readOnly: false, want: held},
		{sharing: true, readOnly: true, want: held},
		{sharing: false, readOnly: true, want: readOnly},
		{sharing: false, readOnly: false, want: held},
	} {
		var asked string
		err := stillBusy(path, errBusy,
			func(error) bool { return tc.sharing },
			func(p string) bool { asked = p; return tc.readOnly })
		if err == nil || err.Error() != tc.want {
			t.Errorf("sharing %v, read-only %v: %v, want %q", tc.sharing, tc.readOnly, err, tc.want)
		}
		if !tc.sharing && asked != path {
			t.Errorf("sharing %v: looked at %q, want %q", tc.sharing, asked, path)
		}
	}
}

// A file whose mode has no write bit is read-only, as Windows shows the
// read-only attribute; a missing file is not.
func TestReadOnlyFile(t *testing.T) {
	dir := t.TempDir()
	ro, rw := filepath.Join(dir, "ro"), filepath.Join(dir, "rw")
	if err := os.WriteFile(ro, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o644) })
	if err := os.WriteFile(rw, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !readOnlyFile(ro) || readOnlyFile(rw) || readOnlyFile(filepath.Join(dir, "missing")) {
		t.Errorf("readOnlyFile: ro %v, rw %v, missing %v", readOnlyFile(ro), readOnlyFile(rw), readOnlyFile(filepath.Join(dir, "missing")))
	}
}

// A chain of links whose last link dangles names the path that is missing,
// not the first hop, which exists.
func TestSaveNamesTheMissingEndOfALinkChain(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.yaml")
	link2 := filepath.Join(dir, "middle.yaml")
	testutil.Symlink(t, "middle.yaml", link)
	testutil.Symlink(t, filepath.Join(dir, "sub", "gone.yaml"), link2)
	err := Save(link, sample())
	if err == nil {
		t.Fatal("Save wrote through a chain that ends nowhere")
	}
	want := link + " is a link to " + filepath.Join(dir, "sub", "gone.yaml") + ", which does not exist."
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should contain %q", err, want)
	}
}
