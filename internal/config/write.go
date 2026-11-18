package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Windows refuses to rename over a file another program has open, such as
// an editor or a virus scanner reading the config. A rename that fails so is
// tried again every renameRetryEvery until renameRetryFor has passed.
const (
	renameRetryEvery = 50 * time.Millisecond
	renameRetryFor   = 500 * time.Millisecond
)

// renameIntoPlace moves a written temp file into place, waiting out a file
// that another program holds open for a moment.
var renameIntoPlace = retryRename(os.Rename, renameBusy, time.Sleep)

// retryRename returns a rename that calls try and, while busy reports its
// error as one that passes, sleeps renameRetryEvery and calls it again, for
// up to renameRetryFor of sleeping. Any other error returns at once.
func retryRename(try func(oldpath, newpath string) error, busy func(error) bool, sleep func(time.Duration)) func(oldpath, newpath string) error {
	return func(oldpath, newpath string) error {
		err := try(oldpath, newpath)
		for waited := time.Duration(0); err != nil && busy(err) && waited < renameRetryFor; waited += renameRetryEvery {
			sleep(renameRetryEvery)
			err = try(oldpath, newpath)
		}
		return err
	}
}

// WriteFileAtomic writes data to path so that a crash leaves either the old
// file or the new one, never a truncated file. When path is a link, it
// writes the file the link resolves to, so the link stays; a link to a file
// that does not exist is an error. It writes a temp file beside that file,
// so the rename stays on one file system, gives it the existing file's mode
// (or mode for a new file), syncs it, renames it into place and syncs the
// directory, as far as it can: the file is in place by then. The temp file
// is removed on any error.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	target, err := filepath.EvalSymlinks(path)
	switch {
	case err == nil:
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		path, mode = target, info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	default:
		if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&fs.ModeSymlink != 0 {
			dest, _ := os.Readlink(path)
			return fmt.Errorf("%s is a link to %s, which does not exist. Restore that file, or remove the link to start afresh.", path, dest)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = renameIntoPlace(tmpName, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		dir.Sync()
		dir.Close()
	}
	return nil
}
