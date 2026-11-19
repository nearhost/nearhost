//go:build windows

package hosts

import "golang.org/x/sys/windows"

// readOnly reports whether the file at path has FILE_ATTRIBUTE_READONLY. A
// file whose attributes cannot be read is not reported as read-only, so the
// write goes on and fails with its own error.
func readOnly(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return false
	}
	return attrs&windows.FILE_ATTRIBUTE_READONLY != 0
}
