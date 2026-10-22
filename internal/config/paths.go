package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nearhost/nearhost/internal/oserr"
)

// Dir is the nearhost configuration directory: $XDG_CONFIG_HOME/nearhost
// when that is absolute, and otherwise %APPDATA%\nearhost on Windows or
// ~/.config/nearhost elsewhere. A relative XDG_CONFIG_HOME is ignored, as the
// XDG base directory specification requires.
func Dir() (string, error) { return configDirFor(runtime.GOOS) }

// configDirFor is Dir on the operating system goos.
func configDirFor(goos string) (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "nearhost"), nil
	}
	if goos == "windows" {
		return windowsDir("APPDATA")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nearhost"), nil
}

// windowsDir is the nearhost directory in the folder the environment
// variable name gives, which must be absolute.
func windowsDir(name string) (string, error) {
	dir := os.Getenv(name)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%%%s%% is not set to an absolute path", name)
	}
	return filepath.Join(dir, "nearhost"), nil
}

func sub(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// DefaultPath is the config file location.
func DefaultPath() (string, error) { return sub("config.yaml") }

// RunDir holds SSH control sockets.
func RunDir() (string, error) { return sub("run") }

// DataDir is the nearhost data directory: $XDG_DATA_HOME/nearhost when that is
// absolute and otherwise ~/Library/Application Support/nearhost on macOS,
// %LOCALAPPDATA%\nearhost on Windows or ~/.local/share/nearhost elsewhere. A relative XDG_DATA_HOME is ignored, as
// for XDG_CONFIG_HOME. It holds the local CA's private key, which must not
// live in ~/.config: people often keep that folder in a dotfiles repository,
// and anyone with the key can sign a certificate this machine trusts.
func DataDir() (string, error) { return dataDirFor(runtime.GOOS) }

// dataDirFor is DataDir on the operating system goos.
func dataDirFor(goos string) (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "nearhost"), nil
	}
	if goos == "windows" {
		return windowsDir("LOCALAPPDATA")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "nearhost"), nil
	}
	return filepath.Join(home, ".local", "share", "nearhost"), nil
}

// caddyDirs are where Caddy's storage lives now, in DataDir, and where
// Nearhost 0.7 and earlier kept it, in Dir.
func caddyDirs() (current, old string, err error) {
	data, err := DataDir()
	if err != nil {
		return "", "", err
	}
	old, err = sub("caddy")
	if err != nil {
		return "", "", err
	}
	return filepath.Join(data, "caddy"), old, nil
}

// CaddyDir holds Caddy storage including the local CA. It is in DataDir,
// unless only the directory older versions used in Dir exists: that one stays
// in use, with the CA the user already trusts, until MigrateCaddyDir moves it.
func CaddyDir() (string, error) {
	current, old, err := caddyDirs()
	if err != nil {
		return "", err
	}
	if !exists(current) && exists(old) {
		return old, nil
	}
	return current, nil
}

// MigrateCaddyDir moves the Caddy storage older versions kept in Dir to
// DataDir, so the trusted CA goes with it. running reports whether another
// Nearhost is serving on this machine; while one is, it may be using the old
// directory, so nothing moves and CaddyDir keeps returning the old one. A
// failure is a *MigrateError when it got as far as moving; every failure
// leaves a CA where CaddyDir finds it, so callers may carry on.
//
// A Nearhost 0.7 or earlier started between the check and the move would find
// no directory at the old path and make a fresh CA there. Nothing is deleted
// in that case: this run moves the original, and the other uses its new one.
func MigrateCaddyDir(running func() bool) error {
	current, old, err := caddyDirs()
	if err != nil {
		return err
	}
	if exists(current) || !exists(old) || running() {
		return nil
	}
	return migrateCaddyDir(old, current)
}

// MigrateError is a failure to move the Caddy storage from Old to New.
type MigrateError struct {
	Old, New string
	// Moved says the storage is complete at New and only removing Old,
	// which still holds a copy of the CA's private key, failed.
	Moved bool
	Err   error
}

// Error describes the failure, naming both directories.
func (e *MigrateError) Error() string {
	if e.Moved {
		return fmt.Sprintf("moved the local CA to %s but could not delete %s: %v", e.New, e.Old, e.Err)
	}
	return fmt.Sprintf("move the local CA from %s to %s: %v", e.Old, e.New, e.Err)
}

// Unwrap returns the underlying error.
func (e *MigrateError) Unwrap() error { return e.Err }

// LeftoverCaddyDir returns the Caddy storage older versions kept in Dir when
// it is still there beside the one in DataDir, which is the one in use. It
// still holds a copy of the CA's private key.
func LeftoverCaddyDir() (string, bool) {
	current, old, err := caddyDirs()
	if err != nil || !exists(current) || !exists(old) {
		return "", false
	}
	return old, true
}

// exists reports whether anything is at path. A path that can't be checked
// counts as there, so it is never taken for empty and replaced.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// rename is os.Rename, replaced in tests to act as if old and new were on
// different filesystems.
var rename = os.Rename

// migrateCaddyDir moves the directory old to new unless new exists or old
// does not. Across filesystems, or when old is a symbolic link, it copies the
// real directory, keeping every mode, into a temporary directory beside new,
// renames that to new and only then removes old (only the link, when old is
// one), so a failed copy leaves old as it was and nothing at new. Its errors
// are *MigrateError.
func migrateCaddyDir(old, new string) error {
	if exists(new) || !exists(old) {
		return nil
	}
	fail := func(err error) error { return &MigrateError{Old: old, New: new, Err: err} }
	if err := os.MkdirAll(filepath.Dir(new), 0o700); err != nil {
		return fail(err)
	}
	fi, err := os.Lstat(old)
	if err != nil {
		return fail(err)
	}
	// Renaming a link would move the link alone, which may then point
	// nowhere, and leave the key where it was.
	link := fi.Mode()&fs.ModeSymlink != 0
	src := old
	if link {
		if src, err = filepath.EvalSymlinks(old); err != nil {
			return fail(err)
		}
	} else {
		err := rename(old, new)
		if !oserr.CrossDevice(err) {
			if err != nil {
				return fail(err)
			}
			return nil
		}
	}
	tmp, err := os.MkdirTemp(filepath.Dir(new), "."+filepath.Base(new)+"-")
	if err != nil {
		return fail(err)
	}
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return fail(err)
	}
	if err := rename(tmp, new); err != nil {
		os.RemoveAll(tmp)
		return fail(err)
	}
	remove := os.RemoveAll
	if link {
		remove = os.Remove
	}
	if err := remove(old); err != nil {
		return &MigrateError{Old: old, New: new, Moved: true, Err: err}
	}
	return nil
}

// copyTree copies the directory from into the existing directory to, which
// takes from's mode. Files keep their modes and symbolic links their targets.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if rel != "." {
				if err := os.Mkdir(dst, 0o700); err != nil {
					return err
				}
			}
			// Chmod, unlike Mkdir, is not narrowed by the umask.
			return os.Chmod(dst, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case d.Type().IsRegular():
			return copyFile(path, dst, info.Mode().Perm())
		default:
			return fmt.Errorf("%s is not a file or a directory", path)
		}
	})
}

// copyFile copies the regular file from to the new file to with mode perm,
// flushed to disk before it returns.
func copyFile(from, to string, perm fs.FileMode) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	// Made 0600 and widened afterwards, so the key is never readable by
	// others, even for a moment.
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Chmod(perm); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}
