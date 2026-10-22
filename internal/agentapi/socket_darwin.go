package agentapi

// maxSocketPath is the longest Unix socket path the OS accepts: the size of
// sockaddr_un's sun_path less its terminating NUL. macOS's is 104 bytes.
const maxSocketPath = 103
