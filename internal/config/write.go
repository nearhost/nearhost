package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/nearhost/nearhost/internal/fsretry"
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
var renameIntoPlace = retryRename(os.Rename, fsretry.Busy, time.Sleep)

// retryRename is fsretry.Retry with the config's waits: renameRetryEvery
// apart, for up to renameRetryFor. One still busy at the end returns as a
// *fsretry.StillBusyError.
func retryRename(try func(oldpath, newpath string) error, busy func(error) bool, sleep func(time.Duration)) func(oldpath, newpath string) error {
	return fsretry.Retry(try, busy, sleep, renameRetryEvery, renameRetryFor)
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
		if _, held := errors.AsType[*fsretry.StillBusyError](err); held {
			return stillBusy(path, err, fsretry.Sharing, readOnlyFile)
		}
		return err
	}
	return syncDir(filepath.Dir(path))
}

// stillBusy is WriteFileAtomic's error for a rename into path that was
// still refused when the retries ran out. Windows refuses with a sharing
// violation when another program has the file open, and with access denied
// both when a program opened it without letting others delete it and when
// the file is read-only. So unless sharing says the error is a sharing
// violation, readOnly looks at the file: a read-only file is reported as
// such, and anything else as a file another program holds.
func stillBusy(path string, err error, sharing func(error) bool, readOnly func(string) bool) error {
	if !sharing(err) && readOnly(path) {
		return fmt.Errorf("%s is read-only, so Nearhost cannot save it. Clear the file's read-only attribute and try again.", path)
	}
	return fmt.Errorf("%s is open in another program. Close it and try again.", path)
}

// readOnlyFile reports whether path is a file with no write permission,
// which is how Go shows the read-only attribute on Windows. A path that
// cannot be read is not.
func readOnlyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o200 == 0
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
