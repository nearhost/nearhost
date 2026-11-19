// Package fsretry renames files on Windows, which refuses to rename a file
// that another program, such as an editor or a virus scanner, has open for
// a moment. Off Windows a rename never fails so, and nothing is retried.
package fsretry

import (
	"os"
	"time"
)

// StillBusyError is a rename that another program still blocked when the
// retries ran out.
type StillBusyError struct{ Err error }

// Error returns the rename's own error.
func (e *StillBusyError) Error() string { return e.Err.Error() }

// Unwrap returns the rename's own error.
func (e *StillBusyError) Unwrap() error { return e.Err }

// Rename renames oldpath to newpath and, while Busy reports its error as one
// that passes, tries again every every, for up to limit of waiting.
func Rename(oldpath, newpath string, every, limit time.Duration) error {
	return Retry(os.Rename, Busy, time.Sleep, every, limit)(oldpath, newpath)
}

// Retry returns a rename that calls try and, while busy reports its error
// as one that passes, sleeps every and calls it again, for up to limit of
// sleeping. Any other error returns at once; one still busy at the end
// returns as a *StillBusyError.
func Retry(try func(oldpath, newpath string) error, busy func(error) bool, sleep func(time.Duration), every, limit time.Duration) func(oldpath, newpath string) error {
	return func(oldpath, newpath string) error {
		err := try(oldpath, newpath)
		for waited := time.Duration(0); err != nil && busy(err) && waited < limit; waited += every {
			sleep(every)
			err = try(oldpath, newpath)
		}
		if err != nil && busy(err) {
			return &StillBusyError{err}
		}
		return err
	}
}
