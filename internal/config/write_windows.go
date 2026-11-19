package config

import (
	"errors"

	"golang.org/x/sys/windows"
)

// renameBusy reports whether a rename failed because another program has
// one of the files open, which passes once that program lets go. Windows
// reports a sharing violation, or access denied when the program opened the
// file without letting others delete it.
func renameBusy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// syncDir does nothing on Windows. Go cannot flush a directory handle there
// (FlushFileBuffers on one fails), and NTFS journals the rename itself, so
// there is no step to take and no error worth reporting.
func syncDir(string) error { return nil }
