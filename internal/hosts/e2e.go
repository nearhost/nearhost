//go:build e2e

package hosts

import "os"

// This file is in builds made with the e2e tag alone, which the journeys in
// internal/e2e make and no release does. In such a build the hosts file is
// the one $NEARHOST_E2E_HOSTS names, and one that cannot exist when it names
// none, so a journey never reads or writes the machine's own.
func init() {
	DefaultPath = os.Getenv("NEARHOST_E2E_HOSTS")
	if DefaultPath == "" {
		DefaultPath = os.DevNull + "/nearhost-e2e-hosts-not-set"
	}
}
