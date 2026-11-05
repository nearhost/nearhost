//go:build !darwin && !linux && !windows

package agentapi

// maxSocketPath is the longest Unix socket path the OS accepts: the size of
// sockaddr_un's sun_path less its terminating NUL. The BSDs' sun_path is 104
// bytes, as on macOS.
const maxSocketPath = 103
