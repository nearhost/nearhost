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

	"github.com/nearhost/nearhost/internal/report"
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
	// StartedTUI is a core started in the background by bare nearhost, the
	// terminal interface, because none was running.
	StartedTUI StartedBy = "tui"
	// StartedForeground is a core run in a terminal by nearhost daemon.
	StartedForeground StartedBy = "foreground"
)

// EnvCheck is what the core found about one thing it needs to reach: opening
// links, the SSH agent, Docker or ssh.
type EnvCheck struct {
	// Name is "browser", "ssh agent", "docker" or "ssh".
	Name string `json:"name"`
	// Level is "ok", "info", "warn" or "fail".
	Level  string `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	// Problem is the check as a message, with its code and fix steps; nil
	// when Level is "ok".
	Problem *report.Message `json:"problem,omitempty"`
}

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
	// Problem is why the server is not connected, as a message whose
	// title is Message; nil when there is none.
	Problem *report.Message `json:"problem,omitempty"`
}

// ProjectInfo is the state of one project, as the core last saw it.
type ProjectInfo struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
	URL     string `json:"url"`
	// NetworkURL is the project's address on the network, or "" when the
	// core does not serve it there.
	NetworkURL string `json:"network_url,omitempty"`
	// Problem is Message as a message, with its code and fix; nil when the
	// state needs nothing.
	Problem *report.Message `json:"problem,omitempty"`
}

// Status is what GET /v1/status answers: the core and everything it serves.
type Status struct {
	Info     Info          `json:"info"`
	Servers  []ServerInfo  `json:"servers"`
	Projects []ProjectInfo `json:"projects"`
	// Approvals is what waits for an answer, oldest first, with the
	// .nearhostrc changes after the queue's requests.
	Approvals []ApprovalInfo `json:"approvals"`
	// Page is the address of Nearhost's page, without its key, or "" when
	// the page is not served.
	Page string `json:"page"`
	// Update is the newer release the core has found, without "v", or ""
	// when none is known.
	Update string `json:"update,omitempty"`
	// Network is how the core serves to other devices; nil when the config
	// does not turn networked mode on.
	Network *NetworkInfo `json:"network,omitempty"`
}

// NetworkInfo is how the core serves to the network.
type NetworkInfo struct {
	// On is whether the config turns networked mode on.
	On bool `json:"on"`
	// URL is the page's address on the network, or "" when it is not served.
	URL string `json:"url"`
	// Certificates is "acme" or "ca".
	Certificates string `json:"certificates"`
	// Problem is why the core serves less than the config asks, or "".
	Problem string `json:"problem,omitempty"`
	// ProblemKind is which problem it is: "no-page", "no-token",
	// "bad-token", "no-firewall" or "apply-failed"; "" for none, or from a
	// core that does not say.
	ProblemKind string `json:"problem_kind,omitempty"`
}

// ApprovalInfo is one request that waits for an answer, as status lists it.
type ApprovalInfo struct {
	ID string `json:"id"`
	// Summary says what is asked, in a sentence without its full stop.
	Summary string `json:"summary"`
	// URL is the page's link to answer it, or "" when the page is not served.
	URL string `json:"url"`
}

// errorBody is the part of a failed request's body that clients before
// problem details read: the error's title.
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
