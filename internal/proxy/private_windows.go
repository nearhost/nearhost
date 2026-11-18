package proxy

import (
	"errors"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/winsys"
)

// restrictStorage gives the storage directory an access list only the
// user, SYSTEM and Administrators are on. A Windows folder takes its
// parent's list, which is private under %LOCALAPPDATA% but need not be
// where XDG_DATA_HOME or a redirected profile puts it. Its errors are
// *restrictError. Tests replace it.
var restrictStorage = func(root string) error {
	if err := winsys.RestrictToOwner(root); err != nil {
		return &restrictError{kind: failureKind(err), err: err}
	}
	return nil
}

// failureKind is the kind of winsys.RestrictToOwner's error err.
// ERROR_NOT_SUPPORTED and ERROR_INVALID_FUNCTION are what a file system
// without access lists answers when RestrictToOwner could not tell from the
// volume, as on some network shares.
func failureKind(err error) storageFailure {
	switch {
	case errors.Is(err, winsys.ErrSharedFolder):
		return failedShared
	case errors.Is(err, winsys.ErrNoAccessLists), errors.Is(err, windows.ERROR_NOT_SUPPORTED), errors.Is(err, windows.ERROR_INVALID_FUNCTION):
		return failedNoACLs
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return failedDenied
	}
	return failedOther
}
