package proxy

import (
	"errors"
	"net"
	"sync"
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
	return &socketView{s: s, done: make(chan struct{})}
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
	case <-v.done:
		return nil, net.ErrClosed
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
	set := &handoffSet{addrs: map[string]*handoffAddr{}}
	sock := newSharedSocket(1)
	older := set.wrap(sock.view())
	newer := set.wrap(sock.view())
	c, _ := sock.connect(t) // taken by the pump, waiting for newer's accept
	newer.Close()
	takes(t, accepted(older), c, "the older server")
	older.Close()
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
