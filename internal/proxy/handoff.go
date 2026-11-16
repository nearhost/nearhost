package proxy

import (
	"errors"
	"net"
	"sync"

	"github.com/caddyserver/caddy/v2"
)

// handoffModule is the Caddy listener wrapper module name registered below.
const handoffModule = "nearhost_handoff"

func init() {
	caddy.RegisterModule(Handoff{})
}

// Handoff is a listener wrapper that gives every connection to the newest
// configuration's server, so a request made just after a reload is not
// dropped by the server the reload is stopping.
//
// Where a port cannot be bound twice (SO_REUSEPORT is Unix's), every
// configuration Caddy loads shares the one socket, and each server's accept
// loop takes connections from it. A reload starts the new servers before
// it shuts the old ones down, and wakes an old server's waiting accept by
// setting a deadline in the past on the shared socket. The new server's
// accept wakes too, and clears that deadline, so the old one can stay
// waiting and take the next connection. Its server is shutting down by
// then, and net/http drops a request read on such a server without an
// answer: the client gets EOF.
//
// With this wrapper on each server, one goroutine per address accepts from
// the shared socket, and hands each connection to the newest wrapper still
// open, which is the newest server: no server that is shutting down is
// given a connection. A connection already given to a server before the
// reload started its successor can still be dropped by it.
type Handoff struct{}

// CaddyModule implements caddy.Module.
func (Handoff) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.listeners." + handoffModule,
		New: func() caddy.Module { return new(Handoff) },
	}
}

// WrapListener implements caddy.ListenerWrapper.
func (Handoff) WrapListener(ln net.Listener) net.Listener {
	return handoffs.wrap(ln)
}

var _ caddy.ListenerWrapper = Handoff{}

// handoffs is the process's set of wrapped listeners: Caddy is process-global.
var handoffs = &handoffSet{addrs: map[string]*handoffAddr{}}

// handoffSet holds, per address, the open wrappers of that address, oldest
// first, and whether a goroutine accepts for them.
type handoffSet struct {
	mu    sync.Mutex
	addrs map[string]*handoffAddr
}

type handoffAddr struct {
	open    []*handoffListener
	pumping bool
}

// handoffListener is one server's listener. Its Accept returns what the
// address's accepting goroutine hands it.
type handoffListener struct {
	net.Listener // this server's own view of the shared socket
	set          *handoffSet
	key          string
	conns        chan net.Conn
	errs         chan error
	done         chan struct{}
	closeOnce    sync.Once
}

func (s *handoffSet) wrap(ln net.Listener) net.Listener {
	l := &handoffListener{
		Listener: ln,
		set:      s,
		key:      ln.Addr().Network() + " " + ln.Addr().String(),
		conns:    make(chan net.Conn),
		errs:     make(chan error),
		done:     make(chan struct{}),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.addrs[l.key]
	if a == nil {
		a = &handoffAddr{}
		s.addrs[l.key] = a
	}
	a.open = append(a.open, l)
	if !a.pumping {
		a.pumping = true
		go s.pump(l.key)
	}
	return l
}

// newest is the newest open listener of key, or nil when none is open.
func (s *handoffSet) newest(key string) *handoffListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.addrs[key]; a != nil && len(a.open) > 0 {
		return a.open[len(a.open)-1]
	}
	return nil
}

// stopPumping ends the accepting for key if no listener of it is open,
// reporting whether it did. A wrapper made after that starts it again.
func (s *handoffSet) stopPumping(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.addrs[key]
	if len(a.open) > 0 {
		return false
	}
	delete(s.addrs, key)
	return true
}

// pump accepts the connections of key, through the newest open listener's
// view of the socket, and hands each to the newest open listener, until
// none is open.
func (s *handoffSet) pump(key string) {
	for {
		l := s.newest(key)
		if l == nil {
			if s.stopPumping(key) {
				return
			}
			continue
		}
		c, err := l.Listener.Accept()
		if err != nil {
			if l.closed() {
				continue // accept through the next one
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// A deadline another server's close set on the shared
				// socket to wake its own accept; Caddy has cleared it.
				continue
			}
			l.fail(err)
			continue
		}
		s.deliver(key, c)
	}
}

// deliver hands c to the newest open listener of key, or closes it when
// none is open.
func (s *handoffSet) deliver(key string, c net.Conn) {
	for {
		l := s.newest(key)
		if l == nil {
			c.Close()
			return
		}
		select {
		case l.conns <- c:
			return
		case <-l.done:
			// Closed while c waited: the next one gets it.
		}
	}
}

// fail passes err to l's Accept, unless l closes first.
func (l *handoffListener) fail(err error) {
	select {
	case l.errs <- err:
	case <-l.done:
	}
}

func (l *handoffListener) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// Accept implements net.Listener.
func (l *handoffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errs:
		return nil, err
	case <-l.done:
		return nil, &net.OpError{Op: "accept", Net: l.Addr().Network(), Addr: l.Addr(), Err: net.ErrClosed}
	}
}

// Close implements net.Listener. Connections that arrive after it go to
// the next newest open listener.
func (l *handoffListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		l.set.mu.Lock()
		a := l.set.addrs[l.key]
		for i, o := range a.open {
			if o == l {
				a.open = append(a.open[:i], a.open[i+1:]...)
				break
			}
		}
		l.set.mu.Unlock()
		close(l.done)
		err = l.Listener.Close()
	})
	return err
}
