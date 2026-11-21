// Package control is the local API of the background core: a small versioned
// HTTP API on a Unix socket in the run directory, through which the CLI asks
// the core what it is doing and tells it to stop or to look at the hosts file
// again.
//
// It is not the agent API (internal/agentapi). That one is offered to coding
// agents, and forwarded to servers; this one is for the user's own commands on
// this machine, so it has its own socket, is never forwarded, and is reachable
// only by the user who owns the run directory.
package control

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// APIVersion is the version of the API, the /v1 in its paths. Info reports it,
// so a client can tell a core it cannot talk to from one that is not running.
const APIVersion = 1

// SocketName is the name of the control socket in the run directory.
const SocketName = "control.sock"

// SocketPath is where the control socket of the core with the run directory
// runDir listens.
func SocketPath(runDir string) string {
	return filepath.Join(runDir, SocketName)
}

// StartedBy says how the core came to be running, so a command can say what
// stopping it will undo.
type StartedBy string

const (
	// StartedService is a core started by the login service.
	StartedService StartedBy = "service"
	// StartedDetached is a core started in the background by nearhost -d.
	StartedDetached StartedBy = "detached"
	// StartedTUI is a core that runs inside the terminal interface.
	StartedTUI StartedBy = "tui"
	// StartedForeground is a core run in a terminal by nearhost daemon.
	StartedForeground StartedBy = "foreground"
)

// Info describes a running core.
type Info struct {
	API       int       `json:"api"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	StartedBy StartedBy `json:"startedBy"`
	// Reloads is whether the core applies config changes as they are saved.
	Reloads bool `json:"reloads"`
}

// ServerInfo is the state of one server, as the core last saw it.
type ServerInfo struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// ProjectInfo is the state of one project, as the core last saw it.
type ProjectInfo struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

// Status is what GET /v1/status answers: the core and everything it serves.
type Status struct {
	Info     Info          `json:"info"`
	Servers  []ServerInfo  `json:"servers"`
	Projects []ProjectInfo `json:"projects"`
}

// errorBody is the JSON body of a failed request.
type errorBody struct {
	Error string `json:"error"`
}

// wsaConnRefused is Windows' WSAECONNREFUSED, which a dial of a socket nobody
// listens on fails with there. syscall has no Errno for it on Windows.
const wsaConnRefused = 10061

// wsaNetDown is Windows' WSAENETDOWN. Dialling a unix socket whose folder is
// missing fails with it there, where Linux and macOS say ENOENT.
const wsaNetDown = 10050

// nobodyListens reports whether err from dialling the socket means that
// nothing answers on it: the file is missing, or is left over from a core that
// has gone.
func nobodyListens(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var errno syscall.Errno
	return runtime.GOOS == "windows" && errors.As(err, &errno) && (errno == wsaConnRefused || errno == wsaNetDown)
}
