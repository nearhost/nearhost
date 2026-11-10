package transport

import "context"

// Transport is the connection Nearhost keeps to one server: it carries
// the projects' forwards, the coding agents' socket and SFTP sessions,
// and reconnects on its own until stopped.
type Transport interface {
	// Host is the ssh host the transport reaches.
	Host() string
	// Preflight reports why the transport cannot start, before it launches
	// anything.
	Preflight() error
	// Start begins connecting and keeps the connection up until Stop.
	Start()
	// Stop ends the connection and the supervision of it.
	Stop()
	// WaitConnected blocks until the transport is connected, gives up, or
	// ctx ends.
	WaitConnected(ctx context.Context) error
	// Status is the connection state.
	Status() Status
	// LastError is the latest connection failure, or "".
	LastError() string
	// Health is a snapshot of the connection and the agent socket.
	Health() Health
	// Check runs the non-interactive connectivity check.
	Check(ctx context.Context) error
	// AddForward forwards a local port to a port on the server.
	AddForward(ctx context.Context, localPort, remotePort int) error
	// CancelForward ends a forward AddForward made.
	CancelForward(ctx context.Context, localPort, remotePort int) error
	// Forwards lists the forwards as local and remote port pairs.
	Forwards() [][2]int
	// ForwardStates is the connection state and each forward's state.
	ForwardStates() (Status, []ForwardState)
	// SetAgentForward chooses the socket forwarded for coding agents.
	SetAgentForward(f AgentForward)
	// OpenSFTP opens an SFTP session on the server.
	OpenSFTP(ctx context.Context) (*SFTP, error)
}

var _ Transport = (*Session)(nil)

// Config says which server a Transport reaches and how.
type Config struct {
	Name, Host string
	// RunDir is where a transport keeps its sockets; "" for Check only.
	RunDir string
	// SSHPath is the ssh binary; "" means "ssh".
	SSHPath string
	// Env is extra environment for ssh (tests).
	Env []string
	// Logf receives the transport's log lines; nil logs nothing.
	Logf func(format string, args ...any)
	// Foreground is as Session.Foreground.
	Foreground bool
}

// New returns an idle Transport for c. Call Start to connect.
func New(c Config) Transport {
	s := NewSession(c.Name, c.Host, c.RunDir)
	if c.SSHPath != "" {
		s.SSHPath = c.SSHPath
	}
	s.Env = c.Env
	if c.Logf != nil {
		s.Logf = c.Logf
	}
	s.Foreground = c.Foreground
	return s
}
