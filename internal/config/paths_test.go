package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestDataDirXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if got, err := dataDirFor("linux"); err != nil || got != filepath.Join(xdg, "nearhost") {
		t.Fatalf("absolute XDG_DATA_HOME: got %q, %v", got, err)
	}
	t.Setenv("XDG_DATA_HOME", "relative/data")
	if got, err := dataDirFor("linux"); err != nil || got != filepath.Join(home, ".local", "share", "nearhost") {
		t.Fatalf("relative XDG_DATA_HOME: got %q, %v", got, err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if got, err := dataDirFor("linux"); err != nil || got != filepath.Join(home, ".local", "share", "nearhost") {
		t.Fatalf("no XDG_DATA_HOME: got %q, %v", got, err)
	}
}

func TestDataDirDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// An absolute XDG_DATA_HOME moves the data directory on macOS too, so a
	// source build can keep its CA apart from the installed one.
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if got, err := dataDirFor("darwin"); err != nil || got != filepath.Join(xdg, "nearhost") {
		t.Fatalf("absolute XDG_DATA_HOME: got %q, %v", got, err)
	}
	if runtime.GOOS == "darwin" {
		if got, err := DataDir(); err != nil || got != filepath.Join(xdg, "nearhost") {
			t.Fatalf("DataDir() = %q, %v; want %q", got, err, filepath.Join(xdg, "nearhost"))
		}
	}
	// Otherwise macOS keeps application data in Application Support.
	want := filepath.Join(home, "Library", "Application Support", "nearhost")
	t.Setenv("XDG_DATA_HOME", "relative/data")
	if got, err := dataDirFor("darwin"); err != nil || got != want {
		t.Fatalf("relative XDG_DATA_HOME: got %q, %v; want %q", got, err, want)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if got, err := dataDirFor("darwin"); err != nil || got != want {
		t.Fatalf("no XDG_DATA_HOME: got %q, %v; want %q", got, err, want)
	}
	if runtime.GOOS == "darwin" {
		if got, err := DataDir(); err != nil || got != want {
			t.Fatalf("DataDir() = %q, %v; want %q", got, err, want)
		}
	}
}

// caddyTree makes a fake Caddy storage at dir: directories 0700 and the
// root key 0600, as Caddy writes them. It returns the key's path inside dir.
func caddyTree(t *testing.T, dir, key string) string {
	t.Helper()
	pki := filepath.Join(dir, "pki", "authorities", "local")
	if err := os.MkdirAll(pki, 0o700); err != nil {
		t.Fatal(err)
	}
	for d := pki; d != filepath.Dir(dir); d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pki, "root.key"), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "root.crt"), []byte("cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join("pki", "authorities", "local", "root.key")
}

// checkMoved checks that old is gone and new holds the tree caddyTree made,
// with the modes kept.
func checkMoved(t *testing.T, old, new, key string) {
	t.Helper()
	if _, err := os.Lstat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old directory still there: %v", err)
	}
	fi, err := os.Stat(filepath.Join(new, key))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("root.key mode %v, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(new, key))
	if err != nil || string(data) != "the key" {
		t.Fatalf("root.key = %q, %v", data, err)
	}
	fi, err = os.Stat(filepath.Join(new, "pki", "authorities", "local", "root.crt"))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("root.crt: %v, %v", fi, err)
	}
	for _, d := range []string{new, filepath.Join(new, "pki"), filepath.Join(new, "pki", "authorities", "local")} {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v, %v; want a 0700 directory", d, fi, err)
		}
	}
	fi, err = os.Stat(filepath.Dir(new))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("parent of new: %v, %v; want 0700", fi, err)
	}
}

func TestMigrateMovesOldCaddyDir(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "config", "nearhost", "caddy")
	new := filepath.Join(base, "data", "nearhost", "caddy")
	key := caddyTree(t, old, "the key")
	if err := migrateCaddyDir(old, new); err != nil {
		t.Fatal(err)
	}
	checkMoved(t, old, new, key)
}

func TestMigrateKeepsBothWhenNewExists(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "config", "nearhost", "caddy")
	new := filepath.Join(base, "data", "nearhost", "caddy")
	key := caddyTree(t, old, "old key")
	caddyTree(t, new, "new key")
	if err := migrateCaddyDir(old, new); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{old: "old key", new: "new key"} {
		data, err := os.ReadFile(filepath.Join(dir, key))
		if err != nil || string(data) != want {
			t.Fatalf("%s: root.key = %q, %v; want %q", dir, data, err, want)
		}
	}
}

func TestMigrateDoesNothingWithoutOld(t *testing.T) {
	base := t.TempDir()
	new := filepath.Join(base, "data", "nearhost", "caddy")
	if err := migrateCaddyDir(filepath.Join(base, "missing"), new); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(new); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new made from nothing: %v", err)
	}
}

// exdev makes rename fail as it does between filesystems.
func exdev(t *testing.T) {
	t.Helper()
	real := rename
	rename = func(from, to string) error {
		// The finished copy is moved within the new directory's
		// filesystem, which works.
		if filepath.Dir(from) == filepath.Dir(to) {
			return real(from, to)
		}
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { rename = real })
}

func TestMigrateCopiesAcrossDevices(t *testing.T) {
	exdev(t)
	base := t.TempDir()
	old := filepath.Join(base, "config", "nearhost", "caddy")
	new := filepath.Join(base, "data", "nearhost", "caddy")
	key := caddyTree(t, old, "the key")
	if err := migrateCaddyDir(old, new); err != nil {
		t.Fatal(err)
	}
	checkMoved(t, old, new, key)
	left, err := os.ReadDir(filepath.Dir(new))
	if err != nil || len(left) != 1 {
		t.Fatalf("beside new: %v, %v; want only caddy", left, err)
	}
}

func TestMigrateKeepsOldWhenCopyFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	exdev(t)
	base := t.TempDir()
	old := filepath.Join(base, "config", "nearhost", "caddy")
	new := filepath.Join(base, "data", "nearhost", "caddy")
	key := caddyTree(t, old, "the key")
	unreadable := filepath.Join(old, "pki", "authorities", "local", "root.crt")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })
	err := migrateCaddyDir(old, new)
	var me *MigrateError
	if !errors.As(err, &me) || me.Moved || me.Old != old || me.New != new {
		t.Fatalf("copy of an unreadable file: %v, want a MigrateError not Moved", err)
	}
	if data, err := os.ReadFile(filepath.Join(old, key)); err != nil || string(data) != "the key" {
		t.Fatalf("old root.key = %q, %v", data, err)
	}
	if _, err := os.Lstat(new); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("half a copy left at new: %v", err)
	}
	left, _ := os.ReadDir(filepath.Dir(new))
	if len(left) != 0 {
		t.Fatalf("left beside new: %v", left)
	}
}

func TestCaddyDirPrefersNewThenOld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	data, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	new := filepath.Join(data, "caddy")
	old := filepath.Join(home, "config", "nearhost", "caddy")
	if got, _ := CaddyDir(); got != new {
		t.Fatalf("neither exists: %q, want %q", got, new)
	}
	caddyTree(t, old, "old key")
	if got, _ := CaddyDir(); got != old {
		t.Fatalf("only old exists: %q, want %q", got, old)
	}
	caddyTree(t, new, "new key")
	if got, _ := CaddyDir(); got != new {
		t.Fatalf("both exist: %q, want %q", got, new)
	}
}

func TestMigrateCaddyDirWaitsWhileNearhostRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	old := filepath.Join(home, "config", "nearhost", "caddy")
	key := caddyTree(t, old, "the key")
	if err := MigrateCaddyDir(func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if got, _ := CaddyDir(); got != old {
		t.Fatalf("moved while another Nearhost runs: CaddyDir() = %q", got)
	}
	if err := MigrateCaddyDir(func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	data, _ := DataDir()
	if got, _ := CaddyDir(); got != filepath.Join(data, "caddy") {
		t.Fatalf("CaddyDir() = %q after the move", got)
	}
	checkMoved(t, old, filepath.Join(data, "caddy"), key)
}

func TestMigrateCopiesASymlinkedOldDir(t *testing.T) {
	exdev(t)
	base := t.TempDir()
	real := filepath.Join(base, "dotfiles", "caddy")
	key := caddyTree(t, real, "the key")
	old := filepath.Join(base, "config", "nearhost", "caddy")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	// Relative, as dotfiles managers often make them.
	if err := os.Symlink(filepath.Join("..", "..", "dotfiles", "caddy"), old); err != nil {
		t.Fatal(err)
	}
	new := filepath.Join(base, "data", "nearhost", "caddy")
	if err := migrateCaddyDir(old, new); err != nil {
		t.Fatal(err)
	}
	checkMoved(t, old, new, key)
	if fi, err := os.Lstat(new); err != nil || !fi.IsDir() {
		t.Fatalf("new is not a real directory: %v, %v", fi, err)
	}
	// Only the link goes; what it pointed at is the user's to keep or delete.
	if data, err := os.ReadFile(filepath.Join(real, key)); err != nil || string(data) != "the key" {
		t.Fatalf("link target's root.key = %q, %v", data, err)
	}
}

func TestMigrateReportsAnOldDirItCouldNotDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root deletes from read-only directories")
	}
	exdev(t)
	base := t.TempDir()
	old := filepath.Join(base, "config", "nearhost", "caddy")
	new := filepath.Join(base, "data", "nearhost", "caddy")
	key := caddyTree(t, old, "the key")
	parent := filepath.Dir(old)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	err := migrateCaddyDir(old, new)
	var me *MigrateError
	if !errors.As(err, &me) || !me.Moved || me.Old != old || me.New != new {
		t.Fatalf("got %v, want a Moved MigrateError", err)
	}
	if data, err := os.ReadFile(filepath.Join(new, key)); err != nil || string(data) != "the key" {
		t.Fatalf("new root.key = %q, %v", data, err)
	}
}

func TestLeftoverCaddyDirOnlyWhenBothExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	data, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(home, "config", "nearhost", "caddy")
	caddyTree(t, old, "old key")
	if got, ok := LeftoverCaddyDir(); ok {
		t.Fatalf("old alone is in use, not left over: %q", got)
	}
	caddyTree(t, filepath.Join(data, "caddy"), "new key")
	if got, ok := LeftoverCaddyDir(); !ok || got != old {
		t.Fatalf("both exist: %q, %v; want %q", got, ok, old)
	}
}
