//go:build unix

package elevate

import "os"

// geteuid is the process's effective user id. Tests replace it.
var geteuid = os.Geteuid

// Elevated reports whether this process already has the rights privileged
// steps need: effective uid 0 on Unix, an elevated token on Windows.
func Elevated() bool { return geteuid() == 0 }
