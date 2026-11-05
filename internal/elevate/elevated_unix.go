//go:build unix

package elevate

import "os"

// Elevated reports whether this process already has the rights privileged
// steps need: effective uid 0 on Unix, an elevated token on Windows.
func Elevated() bool { return os.Geteuid() == 0 }
