package dockernet

import "os"

// endpoints lists the named pipes a daemon may listen on, most specific
// first: DOCKER_HOST when it names a pipe, the current docker context's pipe,
// then the pipe Docker Desktop serves its engine on and the one its Linux
// engine has behind it. A DOCKER_HOST or context that is not a pipe on this
// machine (tcp, ssh) is skipped: that daemon's networks are not here, so its
// containers do not come from them. Each pipe is listed once.
func endpoints() []string {
	var out []string
	if pipe, ok := pipePath(os.Getenv("DOCKER_HOST")); ok {
		out = append(out, pipe)
	}
	home, _ := os.UserHomeDir()
	if pipe, ok := pipePath(contextHost(home)); ok {
		out = append(out, pipe)
	}
	out = append(out, `\\.\pipe\docker_engine`, `\\.\pipe\dockerDesktopLinuxEngine`)
	return unique(out)
}
