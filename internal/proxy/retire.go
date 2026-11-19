package proxy

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// retireModule is the Caddy listener wrapper module name registered below.
const retireModule = "nearhost_retire"

func init() {
	caddy.RegisterModule(Retire{})
}

// Retire is a listener wrapper that records which of Caddy's listeners are
// open, so the proxy can wait for the ones a load closes.
//
// Caddy closes a server's listeners in goroutines it does not wait for, so a
// load can return while a listener it is done with is still open. After a
// load that failed, a listener the failed configuration had opened still
// holds its port for that moment, and a start tried again at once finds the
// port in use. After a reload on Unix, where each configuration binds its
// own sockets with SO_REUSEPORT, the kernel goes on queueing some new
// connections on the old configuration's socket for that moment, and resets
// them when it is closed: the client gets EOF or a reset. With this wrapper
// on each server the proxy knows every open listener and when its close has
// returned, and a load waits for those it closes.
//
// A connection that arrives while a reload is running, between the new
// socket's bind and the old one's close, can still be queued on the old
// socket and reset.
//
// It wraps TCP listeners only. The UDP sockets of HTTP/3 are deliberately
// not tracked: Caddy keeps one until its connections end, which nothing
// here waits for.
type Retire struct{}

// CaddyModule implements caddy.Module.
func (Retire) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.listeners." + retireModule,
		New: func() caddy.Module { return new(Retire) },
	}
}

// WrapListener implements caddy.ListenerWrapper.
func (Retire) WrapListener(ln net.Listener) net.Listener {
	return retiring.wrap(ln)
}

var _ caddy.ListenerWrapper = Retire{}

// retiring is the process's set of open listeners: Caddy is process-global.
var retiring = &retireSet{open: map[*retireListener]struct{}{}}

// retireSet holds the wrapped listeners whose Close has not returned.
type retireSet struct {
	mu   sync.Mutex
	open map[*retireListener]struct{}
	// wrapped counts every listener wrapped, open or closed since; tests
	// read it to tell that a load opened one.
	wrapped int
}

// retireListener is one server's listener on one address.
type retireListener struct {
	net.Listener
	set       *retireSet
	closed    chan struct{} // closed once Close has closed the listener
	closeOnce sync.Once
}

func (s *retireSet) wrap(ln net.Listener) net.Listener {
	l := &retireListener{Listener: ln, set: s, closed: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open[l] = struct{}{}
	s.wrapped++
	return l
}

// Close implements net.Listener. The listener counts as open until the
// wrapped listener's Close has returned.
func (l *retireListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		err = l.Listener.Close()
		l.set.mu.Lock()
		delete(l.set.open, l)
		l.set.mu.Unlock()
		close(l.closed)
	})
	return err
}

// snapshot is the listeners open now.
func (s *retireSet) snapshot() map[*retireListener]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[*retireListener]struct{}, len(s.open))
	for l := range s.open {
		out[l] = struct{}{}
	}
	return out
}

// since is the listeners open now that are not in before.
func (s *retireSet) since(before map[*retireListener]struct{}) map[*retireListener]struct{} {
	out := s.snapshot()
	for l := range before {
		delete(out, l)
	}
	return out
}

// settle waits for every listener of ls to close, for releaseWait at most.
// Those still open then are said in the log, once, and forgotten: a listener
// that never closes is not waited for again, so it cannot hold up every
// later load. after says what closed them, for the log line.
func (s *retireSet) settle(ls map[*retireListener]struct{}, after string) {
	left := retired(ls, releaseWait)
	if len(left) == 0 {
		return
	}
	addrs := make([]string, 0, len(left))
	s.mu.Lock()
	for l := range left {
		delete(s.open, l)
		addrs = append(addrs, l.Addr().String())
	}
	s.mu.Unlock()
	slices.Sort(addrs)
	noun := "listeners were"
	if len(left) == 1 {
		noun = "listener was"
	}
	emit("", fmt.Sprintf("%d proxy %s still open %s after %s: %s",
		len(left), noun, releaseWait, after, strings.Join(addrs, ", ")))
}

// retired is waitRetired; tests replace it.
var retired = waitRetired

// waitRetired waits until every listener of ls has closed, or timeout has
// passed, and returns those still open then.
func waitRetired(ls map[*retireListener]struct{}, timeout time.Duration) map[*retireListener]struct{} {
	if len(ls) == 0 {
		return nil
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	for l := range ls {
		select {
		case <-l.closed:
			continue
		case <-t.C:
		}
		break
	}
	var left map[*retireListener]struct{}
	for l := range ls {
		select {
		case <-l.closed:
		default:
			if left == nil {
				left = map[*retireListener]struct{}{}
			}
			left[l] = struct{}{}
		}
	}
	return left
}
