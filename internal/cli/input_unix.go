//go:build !windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// openInput opens the `hosts apply --input` file without following a link
// at its last component, and refuses anything but a regular file, checked
// on the opened file. The directory holding it must not be a link either.
// The file is in a directory other processes of the user can write to, and
// apply may run as root, so a link must not lead it to a file of its own.
// The parent directory's link check is a separate Lstat before the open, so
// a process of the same user could swap the directory between the two; the
// impact is limited to a file named input whose first line could reach the
// report through hosts.Apply's quoting.
func openInput(path string) (*os.File, error) {
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
