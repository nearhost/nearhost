//go:build windows

package cli

// listenAgent serves the agent API for server on a loopback port, as ssh
// cannot forward to a socket whose local path holds a drive's colon. Any
// process here can connect to the port, so only requests with the token,
// which reaches the server through the session, are served.
func (a *agents) listenAgent(server string) (local, token string, err error) {
	return a.api.ListenLoopback(server)
}

// doctorAgentLocal is what the doctor's own agent forward reaches: a
// loopback port, as a Windows path cannot be forwarded to. Nothing listens
// there: the doctor only asks whether the server takes the forward.
func doctorAgentLocal(string) string {
	return "127.0.0.1:1"
}
