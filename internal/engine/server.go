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
	// agentAsked is set once Options.Agent has been asked for the
	// session's agent socket. It is guarded by ensure.
	agentAsked bool
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
	closed, held := e.closed, e.held[srv.Name]
	e.mu.Unlock()
	if closed {
		return nil, errShutDown
	}
	if held {
		return nil, errHeld(srv.Name)
	}
	if c.sess.Status() == transport.StatusAuthFailed {
		// The supervisor has exited, so this returns at once. It clears the
		// failed status; otherwise a waiter would see the old failure before
		// the new supervisor has made its first attempt.
		c.sess.Stop()
	}
	// A run directory too long for a control socket fails the start with
	// that reason, not after a wait for a connection that cannot come.
	if err := c.sess.CheckSocketPath(); err != nil {
		return nil, err
	}
	if !c.agentAsked && e.opts.Agent != nil {
		c.agentAsked = true
		if f, ok := e.opts.Agent(srv.Name); ok {
			c.sess.SetAgentForward(f)
		}
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
// have recovered, and where a session that failed for good has its
// passthroughs closed. The session may hold its forward lock while it logs:
// nothing here may block, so those closes run on a goroutine of their own.
func (e *Engine) serverLog(server, msg string) {
	e.bus.Publish(Event{Server: server, Message: msg})
	e.mu.Lock()
	e.reviewServerLocked(server)
	e.closeOnFailureLocked(server)
	e.mu.Unlock()
}

// reviewServerLocked announces the effective state of every project on
// server. A forward found lost is announced at once, naming its port; who
// holds that port is asked in the background.
func (e *Engine) reviewServerLocked(server string) {
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
			if _, reviewed := run.holders[port]; reviewed {
				continue
			}
			if run.holders == nil {
				run.holders = map[int]heldPort{}
			}
			e.lookup++
			run.holders[port] = heldPort{lookup: e.lookup}
			if e.closed {
				continue // shutting down: the loss is shown, its holder not asked for
			}
			e.lookups.Add(1) // under mu and before closed is set, so before Shutdown waits
			go e.lookupHolder(run, port, e.lookup)
		}
		e.announceLocked(run)
	}
}

// lookupHolder asks which process holds the local port of a forward run has
// lost, and announces the project's error again with the answer. It runs on
// its own goroutine and holds no lock while it asks. By the time the answer
// is there the forward may have been restored or the project stopped, so the
// answer is only used if this lookup is still the one the port waits for and
// the forward is still lost.
func (e *Engine) lookupHolder(run *projectRun, port int, lookup uint64) {
	defer e.lookups.Done()
	holder := e.holderOf(e.ctx, port)
	if holder == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if run.holders[port].lookup != lookup || !e.lostLocked(run)[port] {
		return
	}
	run.holders[port] = heldPort{lookup: lookup, holder: holder}
	e.announceLocked(run)
}

// lostLocked is the set of local ports whose forward a project that follows
// its session has lost on a connected session.
func (e *Engine) lostLocked(run *projectRun) map[int]bool {
	c := e.servers[run.server]
	if !run.followsSession() || c == nil {
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

// ServerHealth is a server's connection: its state and last ssh error, how
// many attempts have failed and when the next is.
type ServerHealth struct {
	transport.Health
	// Held is set while the server is disconnected on purpose.
	Held bool
}

// ServerHealth reports a server's connection as ServerStatus does, with
// the attempts that failed and when the next is.
func (e *Engine) ServerHealth(name string) ServerHealth {
	if name == config.LocalServer {
		return ServerHealth{Health: transport.Health{Status: transport.StatusConnected}}
	}
	e.mu.Lock()
	c, held := e.servers[name], e.held[name]
	e.mu.Unlock()
	if c == nil {
		return ServerHealth{Health: transport.Health{Status: transport.StatusDisconnected}, Held: held}
	}
	return ServerHealth{Health: c.sess.Health(), Held: held}
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
