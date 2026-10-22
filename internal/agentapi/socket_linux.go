package agentapi

// maxSocketPath is the longest Unix socket path the OS accepts: the size of
// sockaddr_un's sun_path less its terminating NUL. Linux's sockaddr_un.sun_path is 108 bytes.
const maxSocketPath = 107
