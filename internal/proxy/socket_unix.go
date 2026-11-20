//go:build !windows

package proxy

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// sharedNetwork is the network the proxy's TCP servers listen on: TCP, with
// one socket per address that every configuration shares (see sockets).
const sharedNetwork = "nearhost-tcp"

func init() {
	caddy.RegisterNetwork(sharedNetwork, sockets.listen)
	// HTTP/3 stays on Caddy's own UDP sockets.
	caddyhttp.RegisterNetworkHTTP3(sharedNetwork, "udp")
}

// listenAddrs are the listen addresses of a server on addrs.
func listenAddrs(addrs []string) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = sharedNetwork + "/" + a
	}
	return out
}

// sockets is the process's set of shared sockets: Caddy is process-global.
var sockets = &socketSet{open: map[string]*socket{}}

// socketSet holds one listening socket per address, for as long as a
// listener of it is open.
//
// Caddy binds each configuration's own socket on Unix, with SO_REUSEPORT,
// and a reload starts the new configuration's before it closes the old
// one's. Between the two the kernel spreads new connections over both, and
// the close resets those still queued on the old socket. Here every
// configuration gets a handle on the one socket of an address instead, as
// Caddy does itself where SO_REUSEPORT is missing: the socket and its queue
// outlive a reload, and Handoff gives each connection to the newest
// configuration. The socket is closed when its last handle is, so a stop,
// or a load that failed, still releases the port.
type socketSet struct {
	mu   sync.Mutex
	open map[string]*socket
}

// socket is one listening socket and the number of its open handles.
type socket struct {
	ln   net.Listener
	addr string
	refs int
}

// listen is the caddy.ListenerFunc of sharedNetwork: a handle on the socket
// of the address, which it binds if none is open.
func (s *socketSet) listen(_ context.Context, _, host, portRange string, portOffset uint, cfg net.ListenConfig) (any, error) {
	na, err := caddy.ParseNetworkAddress("tcp/" + net.JoinHostPort(host, portRange))
	if err != nil {
		return nil, err
	}
	addr := na.JoinHostPort(portOffset)
	s.mu.Lock()
	defer s.mu.Unlock()
	sock := s.open[addr]
	if sock == nil {
		// The socket outlives the configuration that opened it, so it is
		// not bound with that configuration's context.
		ln, err := cfg.Listen(context.Background(), "tcp", addr)
		if err != nil {
			return nil, err
		}
		sock = &socket{ln: ln, addr: addr}
		s.open[addr] = sock
	}
	sock.refs++
	return &socketHandle{Listener: sock.ln, set: s, sock: sock}, nil
}

// release drops a handle on sock, and closes it with its last.
func (s *socketSet) release(sock *socket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sock.refs--; sock.refs > 0 {
		return nil
	}
	delete(s.open, sock.addr)
	return sock.ln.Close()
}

// count is the number of sockets open, for tests.
func (s *socketSet) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.open)
}

// socketHandle is one listener's handle on a shared socket. Closing it
// closes the socket only if it is the last handle open; an Accept already
// waiting goes on waiting on the socket, which Handoff's pump relies on to
// hand what it accepts to a newer listener.
type socketHandle struct {
	net.Listener
	set       *socketSet
	sock      *socket
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// Accept implements net.Listener.
func (h *socketHandle) Accept() (net.Conn, error) {
	if h.closed.Load() {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Addr: h.Addr(), Err: net.ErrClosed}
	}
	return h.Listener.Accept()
}

// Close implements net.Listener. It returns once the socket is closed, if
// this was its last handle, so Retire sees the port released.
func (h *socketHandle) Close() error {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		h.closeErr = h.set.release(h.sock)
	})
	return h.closeErr
}
