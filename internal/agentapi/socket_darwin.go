package agentapi

// maxSocketPath is the longest Unix socket path the OS accepts: the size of
// sockaddr_un's sun_path less its terminating NUL. macOS's sockaddr_un.sun_path is 104 bytes.
const maxSocketPath = 103
