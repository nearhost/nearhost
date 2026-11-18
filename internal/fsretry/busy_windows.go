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
	return Sharing(err) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// Sharing reports whether a rename failed with a sharing violation, which
// only a program holding one of the files open causes. Access denied, the
// other error Busy waits out, also comes from a read-only file, so it says
// less about the cause.
func Sharing(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
