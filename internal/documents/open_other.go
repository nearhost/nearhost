//go:build !linux && !darwin && !windows

package documents

import (
	"errors"
	"os"
)

// openedPath gives errors.ErrUnsupported, as this system has no call that
// names an open file. LocalFS.Open compares the file's identity instead.
func openedPath(*os.File) (string, error) {
	return "", errors.ErrUnsupported
}
