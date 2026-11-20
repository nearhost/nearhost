package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyevents"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// handoffModule is the Caddy listener wrapper module name registered below.
const handoffModule = "nearhost_handoff"

func init() {
	caddy.RegisterModule(Handoff{})
}

// Handoff is a listener wrapper that gives every connection to the newest
// started configuration's server, and holds a reload back until the server
// it is replacing has had the first request of every connection it was
// given reach a handler, so a request made while a reload runs is not
// dropped by the server the reload is stopping.
//
// Every configuration Caddy loads shares the one socket of an address: on
// Unix through sharedNetwork, and elsewhere as Caddy does itself where
// SO_REUSEPORT is missing. Each server's accept loop would take connections
// from it. A reload starts the new servers before it shuts the old ones
// down, and net/http drops, unanswered, a request it reads once its server
// has begun to shut down (net/http's conn.serve returns when shuttingDown
// is true after readRequest, before it calls the handler): the client gets
// EOF. On Windows a stopping server's accept could even stay waiting on the
// shared socket and take a connection after its shutdown had begun, as
// Caddy wakes it with a deadline in the past that the new server's accept
// clears.
//
// With this wrapper on each server, one goroutine per address accepts from
// the shared socket, and hands each connection to the newest live wrapper.
// A wrapper a load opens goes live only when Caddy's "started" event says
// the whole configuration has started (see Started, which each server's
// Handoff subscribes while Caddy provisions it), so no connection
// reaches a server Caddy is still starting, or one of a load that fails.
// The event comes before Caddy begins to stop the old configuration, and
// its handler then waits for the old servers: from then on they are given
// no connection, and each connection they have counts until its first
// request reaches Reached, the first handler of every server, or it goes
// idle after a response, or its HTTP/2 preface is read, or it closes. A
// request that has reached a handler is answered even if the shutdown
// begins at once. The wait is requestWait at most for the whole reload,
// and a connection that has gone requestWait without a request (an idle
// client) is not waited for.
//
// This relies on every server that listens on sharedNetwork carrying this
// wrapper and Reached: a server without them would take connections from
// the shared socket while it shuts down. BuildConfig gives them to each of
// its servers (see handedOff). A server Caddy adds by itself would not have
// them: automatic HTTPS adds one for its redirects when no server listens
// on the http port at the HTTPS listener's network and host, and gives it
// that network, so it would share the socket. None is added today, as
// nearhost_http always listens there; TestEveryHandleOnTheSharedSocketsIsHandedOff
// checks on a running proxy that every handle on the shared sockets is one
// of Handoff's. It also relies on Caddy emitting "started" between starting
// the new configuration and stopping the old one, as Caddy 2.11 does; a
// load that ran without it has its listeners put live as it returns (see
// endLoad), without the old servers waited for.
//
// What remains: HTTP/3 is not handed over, as its UDP sockets are Caddy's
// own (see sharedNetwork), so a QUIC request made during a reload is not
// covered. A reload that drops an address does not wait, so the old
// server takes that address's connections until it shuts down, and drops
// an unread request as before. A request whose bytes arrive only after
// requestWait is dropped if the shutdown has begun. And a request on an
// idle keep-alive connection can still meet the shutdown's close of idle
// connections, as it always could; most clients retry an idempotent
// request then.
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
	srv, ok := ctx.Value(caddyhttp.ServerCtxKey).(*caddyhttp.Server)
	var events *caddyevents.App
	if ok {
		app, err := ctx.App("events")
		events, _ = app.(*caddyevents.App)
		// Caddy lets a module subscribe only while the configuration is
		// provisioned. Each server subscribes; the handler does its work
		// once per load (see handoffSet.started).
		ok = err == nil && events != nil && events.On("started", Started{}) == nil
	}
	if !ok {
		// Caddy no longer lets this module follow its server or learn
		// that a configuration has started: connections are still handed
		// to the newest server, which then gets them at once, but a
		// reload no longer waits for the old one's, which can drop a
		// request. Say so once, so an upgrade of Caddy that breaks this
		// is seen.
		noServerOnce.Do(func() {
			emit("", "the proxy cannot follow its servers' connections, so a request made during a reload can be dropped")
		})
		return nil
	}
	h.conns = newServerConns()
	srv.RegisterConnContext(h.conns.connContext)
	srv.RegisterConnState(h.conns.state)
	return nil
}

// noServerOnce keeps Provision's line about a missing server to one per
// process.
var noServerOnce sync.Once

// WrapListener implements caddy.ListenerWrapper.
func (h *Handoff) WrapListener(ln net.Listener) net.Listener {
	return handoffs.wrapFor(ln, h.conns)
}

var (
	_ caddy.ListenerWrapper = (*Handoff)(nil)
	_ caddy.Provisioner     = (*Handoff)(nil)
)

// requestWait bounds how long a load, once it has started, waits for the
// older servers' connections to have their first request reach a handler,
// and is how long one can go without that before it is taken for idle and
// not waited for. Tests change it.
var requestWait = time.Second

// serverConns follows a server's connections from its Accept until their
// first request reaches a handler.
type serverConns struct {
	mu sync.Mutex
	// accepted counts the connections Accept has returned that net/http
	// has yet to give a context; it does so before it accepts again.
	accepted int
	// unread holds each connection given a context whose first request
	// has not reached a handler.
	unread  map[net.Conn]*handoffConn
	changed chan struct{} // closed, and replaced, when a wait should look again
	// waits counts the settles waiting, so a change no one waits for costs
	// no new channel.
	waits atomic.Int32

	// waiting, when set, is called as settle begins to wait; tests watch it.
	waiting func()
}

func newServerConns() *serverConns {
	return &serverConns{unread: map[net.Conn]*handoffConn{}, changed: make(chan struct{})}
}

// handoffConnKey is the context key of a connection's handoffConn.
type handoffConnKey struct{}

// handoffConn is a connection of a followed server, for Reached.
type handoffConn struct {
	srv    *serverConns
	conn   net.Conn
	since  time.Time   // when net/http gave it a context
	unread atomic.Bool // still in srv.unread
}

// taken records a connection Accept is returning.
func (c *serverConns) taken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accepted++
	c.notify()
}

// connContext is the server's net/http ConnContext hook, which net/http
// calls on every connection Accept returns, before it accepts again. The
// context it returns is the parent of the connection's requests'.
func (c *serverConns) connContext(ctx context.Context, conn net.Conn) context.Context {
	hc := &handoffConn{srv: c, conn: conn, since: time.Now()}
	hc.unread.Store(true)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accepted > 0 {
		c.accepted--
	}
	c.unread[conn] = hc
	c.notify()
	return context.WithValue(ctx, handoffConnKey{}, hc)
}

// state is the server's net/http ConnState hook. A connection idle after a
// response, or with its HTTP/2 preface read, closed or hijacked, has no
// request net/http could still drop. StateActive is no such sign: net/http
// sets it before it checks for a shutdown, and drops the request if one
// has begun. It runs on every request of a keep-alive connection, so it
// takes the lock only to stop following a connection or to wake a wait.
func (c *serverConns) state(conn net.Conn, st http.ConnState) {
	switch st {
	case http.StateIdle, http.StateHijacked, http.StateClosed:
		c.mu.Lock()
		defer c.mu.Unlock()
		if hc := c.unread[conn]; hc != nil {
			c.forget(hc)
		}
	default:
		// A wait looks again, so tests can see that StateActive does not
		// end it.
		if c.waits.Load() > 0 {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.notify()
		}
	}
}

// done records that hc's request has reached a handler. Every request of a
// connection comes here; only the first takes the lock.
func (c *serverConns) done(hc *handoffConn) {
	if !hc.unread.Load() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hc.unread.Load() {
		c.forget(hc)
	}
}

// forget stops following hc. Call it with c.mu held.
func (c *serverConns) forget(hc *handoffConn) {
	hc.unread.Store(false)
	delete(c.unread, hc.conn)
	c.notify()
}

// notify wakes the waiting settles, if any. Call it with c.mu held.
func (c *serverConns) notify() {
	if c.waits.Load() == 0 {
		return
	}
	close(c.changed)
	c.changed = make(chan struct{})
}

// settle waits until every connection of the server has had its first
// request reach a handler, or has gone requestWait without, or deadline
// passes.
func (c *serverConns) settle(deadline time.Time) {
	for {
		c.mu.Lock()
		now := time.Now()
		wait := c.accepted > 0
		next := deadline
		for _, hc := range c.unread {
			if idle := hc.since.Add(requestWait); idle.After(now) {
				wait = true
				if idle.Before(next) {
					next = idle
				}
			}
		}
		if !wait || !now.Before(deadline) {
			c.mu.Unlock()
			return
		}
		// Counted under the lock, so a change made after this look closes
		// the channel taken here.
		c.waits.Add(1)
		changed := c.changed
		c.mu.Unlock()
		if c.waiting != nil {
			c.waiting()
		}
		t := time.NewTimer(next.Sub(now))
		select {
		case <-changed:
		case <-t.C:
		}
		t.Stop()
		c.waits.Add(-1)
	}
}

// handoffs is the process's set of wrapped listeners: Caddy is process-global.
var handoffs = &handoffSet{addrs: map[string]*handoffAddr{}}

// handoffSet holds, per address, the open wrappers of that address, oldest
// first. An address has an entry exactly while a goroutine accepts for it.
type handoffSet struct {
	mu    sync.Mutex
	addrs map[string]*handoffAddr
	// loading is set while a load Nearhost began runs, and load counts the
	// loads: a listener wrapped during one is given connections only once
	// Caddy has started the whole configuration (see started).
	loading bool
	load    uint64
	changed chan struct{} // closed, and replaced, as listeners go live or close

	// delivering, when set, is called as the accepting goroutine offers a
	// connection to l; tests watch it.
	delivering func(l *handoffListener)
}

// beginLoad starts a load. Call it with caddyMu held, as Caddy runs one load
// at a time.
func (s *handoffSet) beginLoad() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loading = true
	s.load++
}

// endLoad ends the load beginLoad started; loaded reports that Caddy ran
// it. Listeners of a load that failed before it started stay out of the
// handoff until Caddy closes them. Those of a load that ran but whose
// "started" event never reached Started go live now, so the proxy is not
// left with no server given connections; the old servers were not waited
// for, which is said in the log, once.
func (s *handoffSet) endLoad(loaded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loading = false
	if !loaded {
		return
	}
	missed := false
	for _, a := range s.addrs {
		for _, l := range a.open {
			if !l.live && l.load == s.load {
				l.live, missed = true, true
			}
		}
	}
	if missed {
		s.notify()
		missedStartedOnce.Do(func() {
			emit("", "the proxy did not hear that its configuration had started, so a request made during that reload could be dropped")
		})
	}
}

// missedStartedOnce keeps endLoad's line to one per process.
var missedStartedOnce sync.Once

// started puts the listeners of the load that is running into the handoff,
// newest of their addresses, and then waits, requestWait at most in all,
// for the servers of the listeners they succeed to have the first request
// of every connection they were given reach a handler. Caddy calls it, by
// way of Started, once it has started the new configuration and before it
// begins to stop the old one.
func (s *handoffSet) started() {
	s.mu.Lock()
	var older []*serverConns
	seen := map[*serverConns]bool{}
	for _, a := range s.addrs {
		var promoted *serverConns
		live := false
		for _, l := range a.open {
			if !l.live && l.load == s.load {
				l.live, live = true, true
				promoted = l.srv
			}
		}
		if !live {
			continue
		}
		for _, l := range a.open {
			if l.live && l.load != s.load && l.srv != nil && l.srv != promoted && !seen[l.srv] {
				seen[l.srv] = true
				older = append(older, l.srv)
			}
		}
	}
	s.notify()
	s.mu.Unlock()
	// No older listener's Accept returns a connection from here on, so
	// what its server has is all it will get.
	deadline := time.Now().Add(requestWait)
	for _, o := range older {
		o.settle(deadline)
	}
}

func (s *handoffSet) notify() {
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
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
	load         uint64 // the load that wrapped it, 0 outside one
	live         bool   // given connections; guarded by set.mu
	conns        chan net.Conn
	errs         chan error
	done         chan struct{}
	closeOnce    sync.Once
}

// wrap is wrapFor a server whose connections are not followed.
func (s *handoffSet) wrap(ln net.Listener) net.Listener {
	return s.wrapFor(ln, nil)
}

// wrapFor wraps ln, a listener of the server whose connections are srv. One
// wrapped during a load is given connections only once the load has
// started; one wrapped outside a load, or of a server that is not followed,
// at once.
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
	defer s.mu.Unlock()
	if s.loading && srv != nil {
		l.load = s.load
	} else {
		l.live = true
		s.notify()
	}
	a := s.addrs[l.key]
	if a == nil {
		a = &handoffAddr{}
		s.addrs[l.key] = a
		go s.pump(l.key)
	}
	a.open = append(a.open, l)
	return l
}

// newestLive is the newest open listener of a that is given connections.
// Call it with s.mu held.
func (a *handoffAddr) newestLive() *handoffListener {
	for i := len(a.open) - 1; i >= 0; i-- {
		if a.open[i].live {
			return a.open[i]
		}
	}
	return nil
}

// take reports whether l may return a connection from Accept, as it is
// still the newest live listener of its address, and records it as taken.
func (s *handoffSet) take(l *handoffListener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.addrs[l.key]
	if a == nil || a.newestLive() != l {
		return false
	}
	if l.srv != nil {
		l.srv.taken()
	}
	return true
}

// newest is the newest open listener of key, live or not, or nil when none
// is open: the accepting goroutine accepts through its view of the socket.
func (s *handoffSet) newest(key string) *handoffListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.addrs[key]; a != nil && len(a.open) > 0 {
		return a.open[len(a.open)-1]
	}
	return nil
}

// recipient is the newest live listener of key; or, when none is live but
// some is open, nil and a channel closed once that may have changed; or nil
// and nil when none is open.
func (s *handoffSet) recipient(key string) (*handoffListener, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.addrs[key]
	if a == nil || len(a.open) == 0 {
		return nil, nil
	}
	if l := a.newestLive(); l != nil {
		return l, nil
	}
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return nil, s.changed
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
			s.fail(key, err)
			continue
		}
		s.deliver(key, c)
	}
}

// deliver hands c to the newest live listener of key, waiting for one
// while those open are not yet live, or closes it when none is open.
func (s *handoffSet) deliver(key string, c net.Conn) {
	for {
		l, changed := s.recipient(key)
		if l == nil {
			if changed == nil {
				c.Close()
				return
			}
			<-changed
			continue
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

// fail passes err, an error accepting the connections of key, to the
// newest live listener's Accept, as that server would have taken the
// connection: one not yet live is still being started, and an error that
// ended its accept loop would leave it serving nothing once live. While
// none is live but some is open, it waits for one; with none open, or if
// that listener closes first, the error is dropped, and the next accept
// says what is wrong if anything still is.
func (s *handoffSet) fail(key string, err error) {
	for {
		l, changed := s.recipient(key)
		if l != nil {
			l.fail(err)
			return
		}
		if changed == nil {
			return
		}
		<-changed
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
// the next newest live listener.
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
		// A connection waiting for a live listener looks again, and is
		// closed if none is left open.
		l.set.notify()
		l.set.mu.Unlock()
		close(l.done)
		err = l.Listener.Close()
	})
	return err
}

// reachedModule is the Caddy handler module Handoff needs beside the
// listener wrapper, registered below.
const reachedModule = "nearhost_reached"

func init() {
	caddy.RegisterModule(Reached{})
}

// Started handles Caddy's "started" event, which Caddy emits once it has
// started every app of a new configuration and before it begins to stop
// the old one: it puts the new listeners into the handoff and waits for the
// old servers (see handoffSet.started). Until then the new servers are
// given no connection, so none reaches a server Caddy is still starting,
// or one whose start fails.
type Started struct{}

// Handle implements caddyevents.Handler.
func (Started) Handle(context.Context, caddy.Event) error {
	handoffs.started()
	return nil
}

var _ caddyevents.Handler = Started{}

// Reached is a handler, first in each server's routes, that records that a
// connection's request has reached the handlers. net/http drops a request
// it has read once its server has begun to shut down, but only before it
// calls the server's handler: once a request is here, it will be answered.
type Reached struct{}

// CaddyModule implements caddy.Module.
func (Reached) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + reachedModule,
		New: func() caddy.Module { return new(Reached) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (Reached) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if hc, ok := r.Context().Value(handoffConnKey{}).(*handoffConn); ok {
		hc.srv.done(hc)
	}
	return next.ServeHTTP(w, r)
}

var _ caddyhttp.MiddlewareHandler = Reached{}
