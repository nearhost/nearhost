package proxy

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// handoffModule is the Caddy listener wrapper module name registered below.
const handoffModule = "nearhost_handoff"

func init() {
	caddy.RegisterModule(Handoff{})
}

// Handoff is a listener wrapper that gives every connection to the newest
// configuration's server, and holds a reload back until the server it is
// replacing has read a request from every connection it was given, so a
// request made while a reload runs is not dropped by the server the reload
// is stopping.
//
// Every configuration Caddy loads shares the one socket of an address: on
// Unix through sharedNetwork, and elsewhere as Caddy does itself where
// SO_REUSEPORT is missing. Each server's accept loop would take connections
// from it. A reload starts the new servers before it shuts the old ones
// down, and net/http drops, unanswered, a request it reads once its server
// has begun to shut down: the client gets EOF. On Windows a stopping
// server's accept could even stay waiting on the shared socket and take a
// connection after its shutdown had begun, as Caddy wakes it with a
// deadline in the past that the new server's accept clears.
//
// With this wrapper on each server, one goroutine per address accepts from
// the shared socket, and hands each connection to the newest wrapper still
// open, which is the newest server: an older server is given none once a
// newer one has its listener. Then, before the newer server takes its
// first connection, and so before Caddy can begin to shut the older one
// down, the newer listener waits for the older server's connections to
// leave net/http's StateNew, which they do once their first request has
// been read; each new listener waits for at most requestWait. A connection
// that has had no request for requestWait (an idle client) is not waited
// for, and if its request comes after the shutdown has begun it is dropped
// as before. So, in principle, is one whose goroutine is held up between
// net/http's state hook and its check for a shutdown for the whole rest of
// the reload: the hook runs just before the check, and nothing later and
// before the handler can be watched. HTTP/2 connections leave StateNew as
// they start, so they are not waited for; an HTTP/2 server shuts down with
// a GOAWAY of its own rather than net/http's drop.
type Handoff struct {
	conns *serverConns // the server's connections; nil if it has none
}

// CaddyModule implements caddy.Module.
func (Handoff) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.listeners." + handoffModule,
		New: func() caddy.Module { return new(Handoff) },
	}
}

// Provision implements caddy.Provisioner. It follows the states of the
// server's connections, which Caddy lets a module of the server do before
// the server starts.
func (h *Handoff) Provision(ctx caddy.Context) error {
	if srv, ok := ctx.Value(caddyhttp.ServerCtxKey).(*caddyhttp.Server); ok {
		h.conns = newServerConns()
		srv.RegisterConnState(h.conns.state)
	}
	return nil
}

// WrapListener implements caddy.ListenerWrapper.
func (h *Handoff) WrapListener(ln net.Listener) net.Listener {
	return handoffs.wrapFor(ln, h.conns)
}

var (
	_ caddy.ListenerWrapper = (*Handoff)(nil)
	_ caddy.Provisioner     = (*Handoff)(nil)
)

// requestWait bounds how long a new listener waits for the older server's
// connections to have their first request read, and is how long one can go
// without a request before it is taken for idle and not waited for. Tests
// change it.
var requestWait = time.Second

// serverConns follows a server's connections from its Accept until net/http
// has read their first request.
type serverConns struct {
	mu sync.Mutex
	// accepted counts the connections Accept has returned that net/http
	// has yet to record as new; it does so before it accepts again.
	accepted int
	// fresh holds each connection in StateNew, and when it got there.
	fresh   map[net.Conn]time.Time
	changed chan struct{} // closed, and replaced, at every change

	// waiting, when set, is called as settle begins to wait; tests watch it.
	waiting func()
}

func newServerConns() *serverConns {
	return &serverConns{fresh: map[net.Conn]time.Time{}, changed: make(chan struct{})}
}

// taken records a connection Accept is returning.
func (c *serverConns) taken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accepted++
	c.notify()
}

// state is the server's net/http ConnState hook.
func (c *serverConns) state(conn net.Conn, st http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st == http.StateNew {
		if c.accepted > 0 {
			c.accepted--
		}
		c.fresh[conn] = time.Now()
	} else {
		delete(c.fresh, conn)
	}
	c.notify()
}

func (c *serverConns) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// settle waits until every connection of the server has had its first
// request read, or has gone requestWait without one, or deadline passes.
func (c *serverConns) settle(deadline time.Time) {
	for {
		c.mu.Lock()
		now := time.Now()
		wait := c.accepted > 0
		next := deadline
		for _, since := range c.fresh {
			if idle := since.Add(requestWait); idle.After(now) {
				wait = true
				if idle.Before(next) {
					next = idle
				}
			}
		}
		changed := c.changed
		c.mu.Unlock()
		if !wait || !now.Before(deadline) {
			return
		}
		if c.waiting != nil {
			c.waiting()
		}
		t := time.NewTimer(next.Sub(now))
		select {
		case <-changed:
		case <-t.C:
		}
		t.Stop()
	}
}

// handoffs is the process's set of wrapped listeners: Caddy is process-global.
var handoffs = &handoffSet{addrs: map[string]*handoffAddr{}}

// handoffSet holds, per address, the open wrappers of that address, oldest
// first. An address has an entry exactly while a goroutine accepts for it.
type handoffSet struct {
	mu    sync.Mutex
	addrs map[string]*handoffAddr

	// delivering, when set, is called as the accepting goroutine offers a
	// connection to l; tests watch it.
	delivering func(l *handoffListener)
}

type handoffAddr struct {
	open []*handoffListener
}

// handoffListener is one server's listener. Its Accept returns what the
// address's accepting goroutine hands it.
type handoffListener struct {
	net.Listener // this server's own view of the shared socket
	set          *handoffSet
	srv          *serverConns // nil if not followed
	key          string
	conns        chan net.Conn
	errs         chan error
	done         chan struct{}
	closeOnce    sync.Once
}

// wrap is wrapFor a server whose connections are not followed.
func (s *handoffSet) wrap(ln net.Listener) net.Listener {
	return s.wrapFor(ln, nil)
}

// wrapFor wraps ln, a listener of the server whose connections are srv,
// and makes it the newest of its address. It returns once the servers of
// the older listeners have settled their connections (see Handoff).
func (s *handoffSet) wrapFor(ln net.Listener, srv *serverConns) net.Listener {
	l := &handoffListener{
		Listener: ln,
		set:      s,
		srv:      srv,
		key:      ln.Addr().Network() + " " + ln.Addr().String(),
		conns:    make(chan net.Conn),
		errs:     make(chan error),
		done:     make(chan struct{}),
	}
	s.mu.Lock()
	a := s.addrs[l.key]
	if a == nil {
		a = &handoffAddr{}
		s.addrs[l.key] = a
		go s.pump(l.key)
	}
	var older []*serverConns
	for _, o := range a.open {
		if o.srv != nil && o.srv != srv {
			older = append(older, o.srv)
		}
	}
	a.open = append(a.open, l)
	s.mu.Unlock()
	// No older listener's Accept returns a connection from here on, so
	// what its server has is all it will get.
	deadline := time.Now().Add(requestWait)
	for _, o := range older {
		o.settle(deadline)
	}
	return l
}

// take reports whether l may return a connection from Accept, as it is
// still the newest open listener of its address, and records it as taken.
func (s *handoffSet) take(l *handoffListener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.addrs[l.key]
	if a == nil || len(a.open) == 0 || a.open[len(a.open)-1] != l {
		return false
	}
	if l.srv != nil {
		l.srv.taken()
	}
	return true
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
		if s.delivering != nil {
			s.delivering(l)
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

// Accept implements net.Listener. A connection offered to l as a newer
// listener opened is passed on to the newest, so that a server's
// connections are all taken before a newer listener waits for them.
func (l *handoffListener) Accept() (net.Conn, error) {
	for {
		select {
		case c := <-l.conns:
			if l.set.take(l) {
				return c, nil
			}
			go l.set.deliver(l.key, c)
		case err := <-l.errs:
			return nil, err
		case <-l.done:
			return nil, &net.OpError{Op: "accept", Net: l.Addr().Network(), Addr: l.Addr(), Err: net.ErrClosed}
		}
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
