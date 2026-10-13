package engine

import (
	"errors"
	"fmt"
	"sync"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

var errShutDown = errors.New("engine is shut down")

// serverConn is the SSH session of one remote server. It is created the first
// time a project on the server starts and lives until Shutdown.
type serverConn struct {
	ensure sync.Mutex // serialises (re)starting the session
	sess   *transport.Session
}

// ensureSession returns the server's session with its supervisor running. A
// session whose supervisor gave up (authentication failed) is started afresh,
// so starting a project again retries the connection. After Shutdown it
// starts nothing and returns an error.
func (e *Engine) ensureSession(srv config.Server) (*transport.Session, error) {
	e.mu.Lock()
	c := e.servers[srv.Name]
	if c == nil && !e.closed {
		s := transport.NewSession(srv.Name, srv.SSHHost, e.opts.RunDir)
		if e.opts.SSHPath != "" {
			s.SSHPath = e.opts.SSHPath
		}
		s.Env = e.opts.SSHEnv
		s.Logf = func(format string, args ...any) {
			e.serverLog(srv.Name, fmt.Sprintf(format, args...))
		}
		c = &serverConn{sess: s}
		e.servers[srv.Name] = c
	}
	e.mu.Unlock()
	if c == nil {
		return nil, errShutDown
	}

	c.ensure.Lock()
	defer c.ensure.Unlock()
	// Shutdown stops each session under its ensure lock after marking the
	// engine closed, so a session is never started behind its back.
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return nil, errShutDown
	}
	if c.sess.Status() == transport.StatusAuthFailed {
		// The supervisor has exited, so this returns at once. It clears the
		// failed status; otherwise a waiter would see the old failure before
		// the new supervisor has made its first attempt.
		c.sess.Stop()
	}
	c.sess.Start()
	return c.sess, nil
}

// stop ends the session's master connection.
func (c *serverConn) stop() {
	c.ensure.Lock()
	defer c.ensure.Unlock()
	c.sess.Stop()
}

// serverLog receives every message of a server's session. The session logs
// each status change after making it, and reports itself connected only once
// its forwards have been re-applied, so this is also where the projects of
// the server are found to have become degraded, to have lost a forward, or to
// have recovered.
func (e *Engine) serverLog(server, msg string) {
	e.bus.Publish(Event{Server: server, Message: msg})
	e.reviewServer(server)
}

// reviewServer announces the effective state of every project on server. For
// a forward that has just been found lost it first asks which process holds
// the local port, outside mu, so the announcement can name it.
func (e *Engine) reviewServer(server string) {
	type lookup struct {
		run  *projectRun
		port int
	}
	var lookups []lookup
	e.mu.Lock()
	for _, name := range e.order {
		run := e.runs[name]
		if run.server != server {
			continue
		}
		lost := e.lostLocked(run)
		for port := range run.holders {
			if !lost[port] {
				delete(run.holders, port) // forwarded again, or gone
			}
		}
		for port := range lost {
			if _, known := run.holders[port]; !known {
				lookups = append(lookups, lookup{run, port})
			}
		}
	}
	e.mu.Unlock()

	holders := make([]string, len(lookups))
	for i, l := range lookups {
		holders[i] = e.holderOf(l.port)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for i, l := range lookups {
		if l.run.holders == nil {
			l.run.holders = map[int]string{}
		}
		l.run.holders[l.port] = holders[i] // "" too: do not ask again
	}
	for _, name := range e.order {
		if run := e.runs[name]; run.server == server {
			e.announceLocked(run)
		}
	}
}

// lostLocked is the set of local ports whose forward a running project has
// lost on a connected session.
func (e *Engine) lostLocked(run *projectRun) map[int]bool {
	c := e.servers[run.server]
	if run.state != StateRunning || c == nil {
		return nil
	}
	status, states := c.sess.ForwardStates()
	if status != transport.StatusConnected {
		return nil
	}
	lost := map[int]bool{}
	for _, f := range lostForwards(run, states) {
		lost[f.local] = true
	}
	return lost
}

// ServerStatus reports a server's connection state and its last ssh error.
// The local server is always connected; a server no project has used yet is
// disconnected.
func (e *Engine) ServerStatus(name string) (transport.Status, string) {
	if name == config.LocalServer {
		return transport.StatusConnected, ""
	}
	e.mu.Lock()
	c := e.servers[name]
	e.mu.Unlock()
	if c == nil {
		return transport.StatusDisconnected, ""
	}
	return c.sess.Status(), c.sess.LastError()
}
