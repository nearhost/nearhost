//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ownerProblem refuses `untrust --delete-key` when Nearhost runs as root on
// a storage directory another user owns, as under sudo with that user's
// environment: the CA there is that user's, and so is the trust in it.
func ownerProblem(dir string) error { return ownerProblemFor(os.Geteuid(), dir) }

// ownerProblemFor is ownerProblem for the effective user id euid.
func ownerProblemFor(euid int, dir string) error {
	if euid != 0 {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil // nothing there to delete
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return nil
	}
	owner := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(owner); err == nil {
		owner = u.Username
	}
	return fmt.Errorf("Nearhost is running as root, but %s belongs to %s. Run `nearhost untrust --delete-key` as %s instead", printable(dir), printable(owner), printable(owner))
}
