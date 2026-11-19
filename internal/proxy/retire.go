package proxy

import (
	"net"
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
// port in use. With this wrapper on each server the proxy knows every open
// listener and when its close has returned, and a load waits for those it
// closes.
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

// retired is waitRetired; tests watch it.
var retired = waitRetired

// waitRetired waits until every listener of ls has closed, or timeout has
// passed, and reports whether all closed.
func waitRetired(ls map[*retireListener]struct{}, timeout time.Duration) bool {
	if len(ls) == 0 {
		return true
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	for l := range ls {
		select {
		case <-l.closed:
		case <-t.C:
			return false
		}
	}
	return true
}
