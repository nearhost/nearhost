package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// sharedSocket stands in for the one socket that every configuration shares
// where a port cannot be bound twice. Each view of it is one server's, as
// Caddy hands each server its own wrapper of the shared listener: any view
// can take any connection.
type sharedSocket struct {
	pending chan net.Conn
	addr    net.Addr
}

func newSharedSocket(port int) *sharedSocket {
	return &sharedSocket{pending: make(chan net.Conn), addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}}
}

// view is one server's listener on the shared socket.
func (s *sharedSocket) view() *socketView {
	return &socketView{s: s, done: make(chan struct{}), errs: make(chan error)}
}

// connect makes a connection arrive on the socket and returns the server's
// end of it, and the client's.
func (s *sharedSocket) connect(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	select {
	case s.pending <- server:
	case <-time.After(10 * time.Second):
		t.Fatal("no one accepted the connection")
	}
	return server, client
}

type socketView struct {
	s         *sharedSocket
	done      chan struct{}
	errs      chan error // an error the next Accept returns
	closeOnce sync.Once
}

func (v *socketView) Accept() (net.Conn, error) {
	select {
	case <-v.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case c := <-v.s.pending:
		return c, nil
	case err := <-v.errs:
		return nil, err
	case <-v.done:
		return nil, net.ErrClosed
	}
}

// fail makes an Accept through v return err.
func (v *socketView) fail(t *testing.T, err error) {
	t.Helper()
	select {
	case v.errs <- err:
	case <-time.After(10 * time.Second):
		t.Fatal("no one accepted through the view")
	}
}

func (v *socketView) Close() error {
	v.closeOnce.Do(func() { close(v.done) })
	return nil
}

func (v *socketView) Addr() net.Addr { return v.s.addr }

// accepted runs an accept loop on ln, as a server does, and reports what it
// takes.
func accepted(ln net.Listener) <-chan net.Conn {
	out := make(chan net.Conn, 16)
	go func() {
		defer close(out)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			out <- c
		}
	}()
	return out
}

// takes waits for got to yield want.
func takes(t *testing.T, got <-chan net.Conn, want net.Conn, who string) {
	t.Helper()
	select {
	case c := <-got:
		if c != want {
			t.Fatalf("%s took another connection", who)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not take the connection", who)
	}
}

// A connection that arrives once a newer server has started goes to it, never
// to an older one, though the older one's accept is waiting too.
func TestHandoffGivesConnectionsToTheNewestServer(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	fromOlder := accepted(older)
	first, _ := sock.connect(t)
	takes(t, fromOlder, first, "the only server")

	newer := set.wrap(sock.view())
	fromNewer := accepted(newer)
	for i := 0; i < 50; i++ {
		c, _ := sock.connect(t)
		takes(t, fromNewer, c, "the newer server")
	}
	older.Close()
	if c, ok := <-fromOlder; ok {
		t.Fatalf("the older server took %v after the newer one started", c)
	}
	newer.Close()
}

// When the newest server closes and an older one stays, as when a config
// fails to start and Caddy keeps the running one, connections go back to
// the older one.
func TestHandoffFallsBackWhenTheNewestServerCloses(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	fromOlder := accepted(older)
	newer := set.wrap(sock.view())
	newer.Close()
	c, _ := sock.connect(t)
	takes(t, fromOlder, c, "the older server")
	older.Close()
}

// A connection taken while no server is ready to accept it waits for one,
// and goes to the next newest if the one it waited for closes.
func TestHandoffPassesAWaitingConnectionOnWhenItsServerCloses(t *testing.T) {
	offered := make(chan *handoffListener, 1)
	set := &handoffSet{addrs: map[string]*handoffAddr{}, delivering: func(l *handoffListener) {
		select {
		case offered <- l:
		default:
		}
	}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	newer := set.wrap(sock.view())
	c, _ := sock.connect(t)
	// The pump has taken c and offers it to newer, whose server is not
	// accepting, before newer closes.
	select {
	case l := <-offered:
		if l != newer {
			t.Fatal("the connection was offered to the older server first")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the connection was never offered")
	}
	newer.Close()
	takes(t, accepted(older), c, "the older server")
	older.Close()
}

// A deadline another server's close sets on the shared socket wakes the
// accept with a timeout. That is not the server's error: it keeps accepting,
// and is given the next connection.
func TestHandoffSwallowsATimeoutFromTheSharedSocket(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	view := sock.view()
	ln := set.wrap(view)
	got := accepted(ln)
	view.fail(t, &net.OpError{Op: "accept", Net: "tcp", Err: os.ErrDeadlineExceeded})
	c, _ := sock.connect(t)
	takes(t, got, c, "the server, after the timeout")
	ln.Close()
}

// Any other accept error reaches the server's Accept, and accepting goes on.
func TestHandoffPassesAnAcceptErrorToTheServer(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	view := sock.view()
	ln := set.wrap(view)
	boom := errors.New("too many open files")
	errs := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		errs <- err
	}()
	view.fail(t, boom)
	select {
	case err := <-errs:
		if err != boom {
			t.Fatalf("Accept = %v, want the socket's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the socket's error never reached Accept")
	}
	c, _ := sock.connect(t)
	takes(t, accepted(ln), c, "the server, after the error")
	ln.Close()
}

// Once every server has closed, the accepting goroutine ends, and a later
// server starts a new one.
func TestHandoffStopsAcceptingWhenEveryServerCloses(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	ln := set.wrap(sock.view())
	ln.Close()
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
	waitUntil(t, 10*time.Second, func() bool {
		set.mu.Lock()
		defer set.mu.Unlock()
		return len(set.addrs) == 0
	}, "the accepting goroutine to end")

	again := set.wrap(sock.view())
	c, _ := sock.connect(t)
	takes(t, accepted(again), c, "a server started after the others closed")
	again.Close()
}

// A load's listeners are given no connection until Caddy has started the
// load: a connection that arrives before then goes to the running server.
// Once started, the new listener takes every connection.
func TestHandoffGivesALoadConnectionsOnceItHasStarted(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	fromOlder := accepted(older)
	set.beginLoad()
	newer := set.wrapFor(sock.view(), newServerConns())
	fromNewer := accepted(newer)
	c, _ := sock.connect(t)
	takes(t, fromOlder, c, "the running server, while the load starts")
	set.started()
	set.endLoad(true)
	c, _ = sock.connect(t)
	takes(t, fromNewer, c, "the started server")
	older.Close()
	newer.Close()
}

// A load that fails before it starts never has its listeners given a
// connection: they go on to the running server until Caddy closes them.
func TestHandoffGivesAFailedLoadNoConnection(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	fromOlder := accepted(older)
	set.beginLoad()
	failed := set.wrapFor(sock.view(), newServerConns())
	fromFailed := accepted(failed)
	set.endLoad(false)
	for i := 0; i < 5; i++ {
		c, _ := sock.connect(t)
		takes(t, fromOlder, c, "the running server")
	}
	failed.Close()
	if c, ok := <-fromFailed; ok {
		t.Fatalf("the failed load's server took %v", c)
	}
	older.Close()
}

// startedAfter runs set.started in a goroutine and reports, once it has
// returned, the value read had then.
func startedAfter(set *handoffSet, read *atomic.Bool) <-chan bool {
	out := make(chan bool, 1)
	go func() {
		set.started()
		out <- read.Load()
	}()
	return out
}

// watchWaits makes conns report each time settle begins to wait.
func watchWaits(conns *serverConns) <-chan struct{} {
	ch := make(chan struct{}, 1)
	conns.waiting = func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return ch
}

// waitsAgain fails the test unless the load's start waits on, rather than
// returning.
func waitsAgain(t *testing.T, waiting <-chan struct{}, opened <-chan bool, what string) {
	t.Helper()
	select {
	case <-waiting:
	case <-opened:
		t.Fatalf("the load stopped waiting for the older server %s", what)
	case <-time.After(10 * time.Second):
		t.Fatalf("the load never waited for the older server %s", what)
	}
}

// Once a load has started, it waits for the older server to have the
// first request of every connection it was given reach a handler, before
// Caddy can begin to shut that server down. The connection counts from the
// moment Accept returns it. net/http's StateActive is not enough: it is
// set before net/http checks for a shutdown, which drops the request.
func TestHandoffWaitsForTheOlderServersRequestsToReachAHandler(t *testing.T) {
	orig := requestWait
	requestWait = time.Minute
	t.Cleanup(func() { requestWait = orig })
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	olderConns := newServerConns()
	older := set.wrapFor(sock.view(), olderConns)
	fromOlder := accepted(older)
	c, _ := sock.connect(t)
	takes(t, fromOlder, c, "the only server")
	waiting := watchWaits(olderConns)

	set.beginLoad()
	newer := set.wrapFor(sock.view(), newServerConns())
	var read atomic.Bool
	opened := startedAfter(set, &read)
	waitsAgain(t, waiting, opened, "before net/http had the connection")
	ctx := olderConns.connContext(context.Background(), c)
	waitsAgain(t, waiting, opened, "once net/http had the connection")
	olderConns.state(c, http.StateActive)
	waitsAgain(t, waiting, opened, "at StateActive")
	read.Store(true)
	olderConns.done(ctx.Value(handoffConnKey{}).(*handoffConn))
	select {
	case ok := <-opened:
		if !ok {
			t.Fatal("the load stopped waiting before the request reached a handler")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the load never stopped waiting")
	}
	set.endLoad(true)
	newer.Close()
	older.Close()
}

// A connection idle after its response, or an HTTP/2 one whose preface
// has been read, has no request net/http could drop, and is not waited for.
func TestHandoffDoesNotWaitForAnIdleConnection(t *testing.T) {
	orig := requestWait
	requestWait = time.Minute
	t.Cleanup(func() { requestWait = orig })
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	olderConns := newServerConns()
	older := set.wrapFor(sock.view(), olderConns)
	fromOlder := accepted(older)
	c, _ := sock.connect(t)
	takes(t, fromOlder, c, "the only server")
	olderConns.connContext(context.Background(), c)
	olderConns.state(c, http.StateActive)
	olderConns.state(c, http.StateIdle)
	waits := 0
	olderConns.waiting = func() { waits++ }
	set.beginLoad()
	newer := set.wrapFor(sock.view(), newServerConns())
	set.started()
	set.endLoad(true)
	if waits != 0 {
		t.Errorf("the load waited %d times for an idle connection", waits)
	}
	newer.Close()
	older.Close()
}

// A load waits requestWait at most in all, however many addresses it
// opens. Each older server here has a connection whose request never
// comes, so each would be waited for until its deadline. With one deadline
// for the load, the server waited for second finds it passed and does not
// wait; with a deadline of each server's own, both would wait.
func TestHandoffWaitsOnceForALoad(t *testing.T) {
	orig := requestWait
	requestWait = 200 * time.Millisecond
	t.Cleanup(func() { requestWait = orig })
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	var olds, news []net.Listener
	var socks []*sharedSocket
	var waited atomic.Int32 // older servers that waited at all
	for port := 1; port <= 2; port++ {
		sock := newSharedSocket(port)
		socks = append(socks, sock)
		srv := newServerConns()
		var once sync.Once
		srv.waiting = func() { once.Do(func() { waited.Add(1) }) }
		old := set.wrapFor(sock.view(), srv)
		olds = append(olds, old)
		c, _ := sock.connect(t)
		takes(t, accepted(old), c, "the only server")
	}
	newer := newServerConns()
	set.beginLoad()
	for _, sock := range socks {
		news = append(news, set.wrapFor(sock.view(), newer))
	}
	set.started()
	set.endLoad(true)
	if n := waited.Load(); n > 1 {
		t.Errorf("%d older servers were waited for, want one at most: the load's deadline is for all of them", n)
	}
	for _, l := range append(olds, news...) {
		l.Close()
	}
}

// If Caddy's "started" event never reaches the handoff, a load that ran
// still has its listeners put live as it ends, and says so in the log:
// otherwise no server would be given a connection again.
func TestHandoffPutsALoadLiveWhenItsStartIsMissed(t *testing.T) {
	rec := &recorder{}
	SetSink(rec)
	t.Cleanup(func() { SetSink(nil) })
	missedStartedOnce = sync.Once{}
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	set.beginLoad()
	newer := set.wrapFor(sock.view(), newServerConns())
	fromNewer := accepted(newer)
	set.endLoad(true)
	older.Close()
	c, _ := sock.connect(t)
	takes(t, fromNewer, c, "the server of the load whose start was missed")
	if !rec.hasFor("", "did not hear that its configuration had started") {
		t.Errorf("log = %q, want the missed start said", rec.lines)
	}
	newer.Close()
}

// A connection taken while the first load is starting waits for its
// listener to go live. If the load fails instead and its listeners close,
// the connection is closed at once, and the address is let go.
func TestHandoffClosesAConnectionWhenAFailedLoadLeavesNoListener(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	set.beginLoad()
	failed := set.wrapFor(sock.view(), newServerConns())
	_, client := sock.connect(t)
	set.endLoad(false)
	failed.Close()
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("the client's read = %v, want EOF as its connection is closed", err)
	}
	waitUntil(t, 10*time.Second, func() bool {
		set.mu.Lock()
		defer set.mu.Unlock()
		return len(set.addrs) == 0
	}, "the accepting goroutine to end")
}

// An accept error goes to the live server, never to one a load is still
// starting, whose accept loop it could end before it went live.
func TestHandoffPassesAnAcceptErrorToTheLiveServer(t *testing.T) {
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	olderView := sock.view()
	older := set.wrap(olderView)
	set.beginLoad()
	view := sock.view()
	newer := set.wrapFor(view, newServerConns())
	newerErrs := make(chan error, 1)
	go func() {
		_, err := newer.Accept()
		newerErrs <- err
	}()
	boom := errors.New("too many open files")
	// The accepting goroutine accepts through whichever view was newest
	// when it began to wait.
	select {
	case olderView.errs <- boom:
	case view.errs <- boom:
	case <-time.After(10 * time.Second):
		t.Fatal("no one accepted through either view")
	}
	errs := make(chan error, 1)
	go func() {
		_, err := older.Accept()
		errs <- err
	}()
	select {
	case err := <-errs:
		if err != boom {
			t.Fatalf("the live server's Accept = %v, want the socket's error", err)
		}
	case err := <-newerErrs:
		t.Fatalf("the server still starting got %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the socket's error never reached the live server")
	}
	set.endLoad(false)
	newer.Close()
	older.Close()
}

// Reached ends the wait for its connection: a request that has reached
// the handlers is answered whatever happens to its server next. The wait
// goes on at StateActive and ends only at Reached.
func TestReachedEndsTheWaitForItsConnection(t *testing.T) {
	orig := requestWait
	requestWait = time.Minute
	t.Cleanup(func() { requestWait = orig })
	srv := newServerConns()
	waiting := watchWaits(srv)
	conn, _ := net.Pipe()
	defer conn.Close()
	ctx := srv.connContext(context.Background(), conn)
	settled := make(chan struct{})
	go func() {
		srv.settle(time.Now().Add(requestWait))
		close(settled)
	}()
	waitsAgain(t, waiting, settledBool(settled), "before the request was read")
	srv.state(conn, http.StateActive)
	waitsAgain(t, waiting, settledBool(settled), "at StateActive")
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })
	if err := (Reached{}).ServeHTTP(httptest.NewRecorder(), req, next); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("the wait went on after the request reached the handlers")
	}
}

// settledBool turns a closed channel into a send of false, for waitsAgain.
func settledBool(settled <-chan struct{}) <-chan bool {
	out := make(chan bool, 1)
	go func() {
		<-settled
		out <- false
	}()
	return out
}
