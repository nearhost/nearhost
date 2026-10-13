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
// each status change after making it, so this is also where the projects of
// the server are found to have become degraded or to have recovered.
func (e *Engine) serverLog(server, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bus.Publish(Event{Server: server, Message: msg})
	for _, name := range e.order {
		if run := e.runs[name]; run.server == server {
			e.announceLocked(run)
		}
	}
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
