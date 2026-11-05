//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openInput opens the `hosts apply --input` file without following a
// reparse point at its last component (a symlink or a junction), and
// refuses a directory or a reparse point, checked on the opened handle. The
// directory holding it must not be a link either. The file is in a
// directory other processes of the user can write to, and apply runs
// elevated, so a link must not lead it to a file of its own.
// The parent directory's link check is a separate Lstat before the open, so
// a process of the same user could swap the directory between the two; the
// impact is limited to a file named input whose first line could reach the
// report through hosts.Apply's quoting.
func openInput(path string) (*os.File, error) {
	if err := refuseLinkedDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("input directory: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
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
	return os.NewFile(uintptr(h), path), nil
}
