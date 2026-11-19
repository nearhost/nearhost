package fsretry

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Busy reports whether a rename failed because another program has one of
// the files open, which passes once that program lets go. Windows reports a
// sharing violation, or access denied when the program opened the file
// without letting others delete it.
func Busy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
