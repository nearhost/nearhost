package config

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// A rename that Windows refuses because another program has the file open
// counts as busy, so it is tried again, whether Windows says so with a
// sharing violation or access denied; a missing file does not.
func TestRenameBusyKnowsAFileHeldOpen(t *testing.T) {
	busy := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_SHARING_VIOLATION}
	if !renameBusy(busy) {
		t.Error("a sharing violation is not busy")
	}
	if !renameBusy(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_ACCESS_DENIED}) {
		t.Error("access denied is not busy")
	}
	if renameBusy(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_FILE_NOT_FOUND}) {
		t.Error("a missing file is busy")
	}
	if renameBusy(errors.New("other")) {
		t.Error("a plain error is busy")
	}
}
