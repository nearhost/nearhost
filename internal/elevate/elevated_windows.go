package elevate

import "golang.org/x/sys/windows"

// Elevated reports whether this process already has the rights privileged
// steps need: effective uid 0 on Unix, an elevated token on Windows.
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
