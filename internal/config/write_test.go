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

// errBusy stands in for a Windows sharing violation.
var errBusy = errors.New("busy")

// A rename that fails twice with a sharing violation is tried again, 50 ms
// apart, and succeeds on the third try.
func TestRetryRenameRetriesASharingViolation(t *testing.T) {
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

// A sharing violation that lasts gives up after 500 ms of waiting and
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
	}, func(err error) bool { return errors.Is(err, errBusy) }, func(time.Duration) { t.Error("slept on an error that is not a sharing violation") })
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
