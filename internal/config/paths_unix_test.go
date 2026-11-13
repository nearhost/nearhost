//go:build unix

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

// crossDeviceErr is what rename fails with between filesystems.
var crossDeviceErr = syscall.EXDEV

func TestPathsHonourXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	d, err := Dir()
	if err != nil || d != "/tmp/xdg/nearhost" {
		t.Fatalf("Dir = %q, %v", d, err)
	}
	p, _ := DefaultPath()
	if p != "/tmp/xdg/nearhost/config.yaml" {
		t.Fatalf("DefaultPath = %q", p)
	}
	r, _ := RunDir()
	if r != "/tmp/xdg/nearhost/run" {
		t.Fatalf("RunDir = %q", r)
	}
}

func TestPathsDefaultToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	d, err := Dir()
	if err != nil || d != filepath.Join(home, ".config", "nearhost") {
		t.Fatalf("Dir = %q, %v", d, err)
	}
}

func TestPathsIgnoreRelativeXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/xdg")
	home, _ := os.UserHomeDir()
	d, err := Dir()
	if err != nil || d != filepath.Join(home, ".config", "nearhost") {
		t.Fatalf("Dir = %q, %v", d, err)
	}
}

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
	left, err := os.ReadDir(filepath.Dir(new))
	if err != nil || len(left) != 0 {
		t.Fatalf("left beside new: %v, %v; want nothing", left, err)
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
