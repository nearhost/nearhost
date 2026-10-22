package agentapi

// maxSocketPath is the longest Unix socket path the OS accepts: the size of
// sockaddr_un's sun_path less its terminating NUL. Windows' is 108 bytes, as on
// Linux.
const maxSocketPath = 107
