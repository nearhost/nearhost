//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// heldDirs are the directories `hosts apply` reads --input from and writes
// --report to, each opened once, before the file in it, and held until
// Close. The elevator puts both files in one new folder in the user's temp
// directory, which other processes of that user can write to, so that
// folder is held from before apply reads its input until the report is
// written. A directory held without FILE_SHARE_DELETE cannot be renamed,
// replaced or deleted, so a file checked to be in it stays where apply
// expects. It also keeps the elevator's cleanup from removing the folder
// itself while apply runs; the files in it can still go, and the empty
// folder is left behind.
type heldDirs struct {
	held map[string]*heldDir
}

// heldDir is one held directory: its handle and its final path, as
// GetFinalPathNameByHandle gives it.
type heldDir struct {
	h     windows.Handle
	final string
}

// hold opens dir without following a reparse point and holds it, or returns
// the handle already held for dir. A symlink or a junction is refused,
// checked on the opened handle.
func (d *heldDirs) hold(dir string) (*heldDir, error) {
	if hd, ok := d.held[dir]; ok {
		return hd, nil
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	// GENERIC_READ includes FILE_LIST_DIRECTORY, a data access, so the
	// share mode is enforced against a rename or delete of dir.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	hd, err := checkHeldDir(h, dir)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if d.held == nil {
		d.held = map[string]*heldDir{}
	}
	d.held[dir] = hd
	return hd, nil
}

// checkHeldDir is the heldDir for the handle h, opened at dir, or an error
// when h is a reparse point or not a directory.
func checkHeldDir(h windows.Handle, dir string) (*heldDir, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("%s is a link", dir)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	final, err := finalPath(h)
	if err != nil {
		return nil, err
	}
	return &heldDir{h: h, final: final}, nil
}

// Close lets go of every held directory.
func (d *heldDirs) Close() {
	for _, hd := range d.held {
		windows.CloseHandle(hd.h)
	}
	d.held = nil
}

// finalPath is the path of the open file h as the file system names it,
// every link and short name resolved (\\?\C:\Users\...).
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		// Flags 0 are FILE_NAME_NORMALIZED and VOLUME_NAME_DOS.
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		// Too small: n is the size needed, with the terminating NUL.
		buf = make([]uint16, n)
	}
}

// holds returns an error unless the open file h is the entry named like
// path's last element directly in hd, by final path. Names are compared
// without case, as NTFS does.
func (hd *heldDir) holds(h windows.Handle, path string) error {
	got, err := finalPath(h)
	if err != nil {
		return err
	}
	want := strings.TrimSuffix(hd.final, `\`) + `\` + filepath.Base(path)
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%s opened as %s, not in its directory %s", path, got, hd.final)
	}
	return nil
}

// openInput opens the `hosts apply --input` file. Its directory is held
// first (see heldDirs) and must not be a symlink or a junction; the file is
// opened without following a reparse point at its last component and
// refused when it is a directory or a reparse point, checked on the opened
// handle, or when its final path is not the held directory's plus its name.
// The file is in a directory other processes of the user can write to, and
// apply runs elevated, so a link must not lead it to a file of its own. What
// remains: a hard link at input passes, as it is a name in that directory,
// and so would a change above the temp directory that the user can make
// anyway. The input is opened with FILE_SHARE_DELETE, so the elevator can
// still remove it when it stops waiting.
func openInput(dirs *heldDirs, path string) (*os.File, error) {
	hd, err := dirs.hold(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("input directory: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if err := hd.holds(h, path); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// createReport creates the `hosts apply --report` file. Its directory is
// held first (see heldDirs) and must not be a symlink or a junction; the
// file is created with CREATE_NEW, as O_EXCL does, which fails on an
// existing file or link rather than following it. When the new file's
// final path is not the held directory's plus its name, it is deleted
// through its own handle and refused. What remains is as for openInput: a
// change above the temp directory that the user can make anyway.
func createReport(dirs *heldDirs, path string) (*os.File, error) {
	hd, err := dirs.hold(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("report directory: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := hd.holds(h, path); err != nil {
		// FILE_DISPOSITION_INFO is one BOOLEAN, DeleteFile.
		del := byte(1)
		derr := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, 1)
		windows.CloseHandle(h)
		return nil, errors.Join(err, derr)
	}
	return os.NewFile(uintptr(h), path), nil
}
