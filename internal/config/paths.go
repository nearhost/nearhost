package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Dir is the nearhost configuration directory. A relative XDG_CONFIG_HOME is
// ignored, as the XDG base directory specification requires.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "nearhost"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nearhost"), nil
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

// DataDir is the nearhost data directory: ~/Library/Application Support/nearhost
// on macOS and, elsewhere, $XDG_DATA_HOME/nearhost when that is absolute or
// ~/.local/share/nearhost. It holds the local CA's private key, which must not
// live in ~/.config: people often keep that folder in a dotfiles repository,
// and anyone with the key can sign a certificate this machine trusts.
func DataDir() (string, error) { return dataDirFor(runtime.GOOS) }

// dataDirFor is DataDir on the operating system goos.
func dataDirFor(goos string) (string, error) {
	if goos != "darwin" {
		if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
			return filepath.Join(xdg, "nearhost"), nil
		}
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
// directory, so nothing moves and CaddyDir keeps returning the old one.
func MigrateCaddyDir(running func() bool) error {
	current, old, err := caddyDirs()
	if err != nil {
		return err
	}
	if exists(current) || !exists(old) || running() {
		return nil
	}
	if err := migrateCaddyDir(old, current); err != nil {
		return fmt.Errorf("move the local CA from %s to %s: %w", old, current, err)
	}
	return nil
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
// does not. Across filesystems it copies old, keeping every mode, into a
// temporary directory beside new, renames that to new and only then removes
// old, so a failed copy leaves old as it was and nothing at new.
func migrateCaddyDir(old, new string) error {
	if exists(new) || !exists(old) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(new), 0o700); err != nil {
		return err
	}
	err := rename(old, new)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(new), "."+filepath.Base(new)+"-")
	if err != nil {
		return err
	}
	if err := copyTree(old, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := rename(tmp, new); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return os.RemoveAll(old)
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
