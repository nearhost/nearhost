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

// stillBusyError is a rename that another program still blocked when the
// retries ran out.
type stillBusyError struct{ err error }

// Error returns the rename's own error.
func (e *stillBusyError) Error() string { return e.err.Error() }

// Unwrap returns the rename's own error.
func (e *stillBusyError) Unwrap() error { return e.err }

// retryRename returns a rename that calls try and, while busy reports its
// error as one that passes, sleeps renameRetryEvery and calls it again, for
// up to renameRetryFor of sleeping. Any other error returns at once; one
// still busy at the end returns as a *stillBusyError.
func retryRename(try func(oldpath, newpath string) error, busy func(error) bool, sleep func(time.Duration)) func(oldpath, newpath string) error {
	return func(oldpath, newpath string) error {
		err := try(oldpath, newpath)
		for waited := time.Duration(0); err != nil && busy(err) && waited < renameRetryFor; waited += renameRetryEvery {
			sleep(renameRetryEvery)
			err = try(oldpath, newpath)
		}
		if err != nil && busy(err) {
			return &stillBusyError{err}
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
// directory (see syncDir). The temp file is removed on any error.
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
			return fmt.Errorf("%s is a link to %s, which does not exist. Restore that file, or remove the link to start afresh.", path, missingEnd(path))
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
		if _, held := errors.AsType[*stillBusyError](err); held {
			return fmt.Errorf("%s is open in another program. Close it and try again.", path)
		}
		return err
	}
	return syncDir(filepath.Dir(path))
}

// maxLinkHops bounds how far missingEnd follows a chain of links.
const maxLinkHops = 40

// missingEnd follows the chain of links that starts at path and returns the
// first path in it that does not exist, so an error names the file that is
// missing rather than a link on the way to it.
func missingEnd(path string) string {
	for range maxLinkHops {
		dest, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		if _, err := os.Lstat(dest); err != nil {
			return dest
		}
		path = dest
	}
	return path
}
