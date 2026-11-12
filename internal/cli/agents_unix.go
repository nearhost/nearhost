//go:build unix

package cli

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

// listenAgent serves the agent API for server on a private socket in the
// run directory, which the server's session forwards there. A socket needs
// no token: only this user can open it.
func (a *agents) listenAgent(server string) (local, token string, err error) {
	// Named by a hash, so any server name makes a short plain file name.
	name := fmt.Sprintf("agent-%x", sha256.Sum256([]byte(server)))[:len("agent-")+12]
	local = filepath.Join(a.runDir, name+".sock")
	if err := a.api.Listen(server, local); err != nil {
		return "", "", err
	}
	return local, "", nil
}

// doctorAgentLocal is what the doctor's own agent forward reaches, in its
// run directory. Nothing listens there: the doctor only asks whether the
// server takes the forward.
func doctorAgentLocal(runDir string) string {
	return filepath.Join(runDir, "agent.sock")
}
