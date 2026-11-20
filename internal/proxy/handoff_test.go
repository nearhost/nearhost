package proxy

import (
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// A newer listener of an address opens only once the older server has had
// the first request read from every connection it was given: until then
// Caddy cannot begin to shut that server down, which would drop the
// request unanswered. The wait counts a connection from the moment Accept
// returns it, before net/http records it as new.
func TestHandoffWaitsForTheOlderServersFirstRequests(t *testing.T) {
	orig := requestWait
	requestWait = time.Minute
	t.Cleanup(func() { requestWait = orig })
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	olderConns, newerConns := newServerConns(), newServerConns()
	older := set.wrapFor(sock.view(), olderConns)
	fromOlder := accepted(older)
	c, _ := sock.connect(t)
	takes(t, fromOlder, c, "the only server")

	waiting := make(chan struct{}, 1)
	olderConns.waiting = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	var read atomic.Bool
	opened := make(chan bool, 1)
	go func() {
		newer := set.wrapFor(sock.view(), newerConns)
		opened <- read.Load()
		newer.Close()
	}()
	select {
	case <-waiting:
	case <-opened:
		t.Fatal("the newer listener opened without waiting for the older server")
	case <-time.After(10 * time.Second):
		t.Fatal("the newer listener never waited for the older server")
	}
	olderConns.state(c, http.StateNew)
	read.Store(true)
	olderConns.state(c, http.StateActive)
	select {
	case ok := <-opened:
		if !ok {
			t.Fatal("the newer listener opened before the older server had read its connection's request")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the newer listener never opened")
	}
	older.Close()
}
