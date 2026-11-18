package proxy

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/winsys"
)

// Each error RestrictToOwner gives has the kind that picks its advice,
// wrapped as RestrictToOwner wraps it.
func TestFailureKindOnWindows(t *testing.T) {
	for _, c := range []struct {
		err  error
		want storageFailure
	}{
		{&os.PathError{Op: "set the access list of", Path: `C:\x`, Err: windows.ERROR_ACCESS_DENIED}, failedDenied},
		{&os.PathError{Op: "set the owner of", Path: `C:\x`, Err: windows.ERROR_ACCESS_DENIED}, failedDenied},
		{fmt.Errorf(`C:\: %w`, winsys.ErrSharedFolder), failedShared},
		{fmt.Errorf(`E:\x: %w`, winsys.ErrNoAccessLists), failedNoACLs},
		{&os.PathError{Op: "set the access list of", Path: `\\s\x`, Err: windows.ERROR_NOT_SUPPORTED}, failedNoACLs},
		{&os.PathError{Op: "set the access list of", Path: `C:\x`, Err: windows.ERROR_FILE_NOT_FOUND}, failedOther},
	} {
		if got := failureKind(c.err); got != c.want {
			t.Errorf("failureKind(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
