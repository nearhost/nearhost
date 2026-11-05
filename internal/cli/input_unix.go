//go:build !windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// heldDirs holds nothing on Unix: the directories of --input and --report
// are checked by path, not held open as on Windows.
type heldDirs struct{}

// Close does nothing, as nothing is held.
func (*heldDirs) Close() {}

// openInput opens the `hosts apply --input` file without following a link
// at its last component, and refuses anything but a regular file, checked
// on the opened file. The directory holding it must not be a link either.
// Only the Windows elevator passes --input; Nearhost's sudo path hands the
// aliases to apply on stdin, so this is here for a hand-run apply. The
// directory's link check is a separate Lstat before the open and no ancestor
// is walked by handle, so a process of the same user could swap the
// directory between the two; the impact is limited to a file named input
// whose first line could reach the report through hosts.Apply's quoting.
func openInput(_ *heldDirs, path string) (*os.File, error) {
	if err := refuseLinkedDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("input directory: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}

// createReport creates the `hosts apply --report` file with O_EXCL, which
// fails on an existing file or link rather than following it, after
// checking that the directory holding it is not a link. That check and the
// create are two steps, so a directory swapped for a link between them
// could still redirect the new file; O_EXCL means nothing is overwritten.
func createReport(_ *heldDirs, path string) (*os.File, error) {
	if err := refuseLinkedDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("report directory: %w", err)
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// refuseLinkedDir returns an error when dir is a symlink. It looks at dir by
// path when it is called, so it says nothing about dir a moment later.
func refuseLinkedDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%s is a link", dir)
	}
	return nil
}
