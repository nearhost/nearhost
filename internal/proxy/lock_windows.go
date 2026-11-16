package proxy

import (
	"os"

	"github.com/nearhost/nearhost/internal/oserr"
)

// lockSettling reports whether a failed lock acquisition may have met the
// lock file in the middle of an unlock, so another attempt can succeed: the
// file is pending deletion still, or by now it is gone, or it can be read
// again. certmagic does not pass the system's error on, and Windows refuses
// with ACCESS_DENIED both a file pending deletion and one the process has no
// right to; only the second lasts, and it is the one this reports false for.
// A read refused by another handle (a sharing violation) is transient, and
// passes too, as the file can be read again once that handle closes.
//
// This is deliberately broad: pending deletion, missing, readable again and
// in use all pass, so any of certmagic's failures around an unlock is tried
// again. Lock bounds that, returning the original error at its deadline, and
// a lasting ACCESS_DENIED (no right to the file) fails at once.
func lockSettling(file string) bool {
	if oserr.DeletePending(file) {
		return true
	}
	f, err := os.Open(file)
	if err == nil {
		f.Close()
		return true
	}
	return os.IsNotExist(err) || oserr.InUse(err)
}
