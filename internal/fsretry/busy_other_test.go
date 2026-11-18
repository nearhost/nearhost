//go:build !windows

package fsretry

import (
	"errors"
	"testing"
)

// Off Windows no rename is refused for a file held open, so nothing is
// busy and nothing is a sharing violation.
func TestNothingIsBusyOffWindows(t *testing.T) {
	if Busy(errors.New("x")) || Sharing(errors.New("x")) {
		t.Error("an error off Windows counts as busy or sharing")
	}
}
