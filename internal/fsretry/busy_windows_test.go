package fsretry

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// A rename that Windows refuses because another program has the file open
// counts as busy, so it is tried again, whether Windows says so with a
// sharing violation or access denied; a missing file does not.
func TestBusyKnowsAFileHeldOpen(t *testing.T) {
	busy := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_SHARING_VIOLATION}
	if !Busy(busy) {
		t.Error("a sharing violation is not busy")
	}
	if !Busy(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_ACCESS_DENIED}) {
		t.Error("access denied is not busy")
	}
	if Busy(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_FILE_NOT_FOUND}) {
		t.Error("a missing file is busy")
	}
	if Busy(errors.New("other")) {
		t.Error("a plain error is busy")
	}
}

// Only a sharing violation is Sharing: access denied also comes from a
// read-only file.
func TestSharingIsOnlyASharingViolation(t *testing.T) {
	if !Sharing(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_SHARING_VIOLATION}) {
		t.Error("a sharing violation is not Sharing")
	}
	if Sharing(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_ACCESS_DENIED}) {
		t.Error("access denied is Sharing")
	}
}
