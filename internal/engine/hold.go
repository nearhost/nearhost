package engine

import (
	"errors"
	"fmt"

	"github.com/nearhost/nearhost/internal/config"
)

// errHeld is the error of starting a session that was disconnected on
// purpose.
func errHeld(name string) error {
	return fmt.Errorf("%s was disconnected; connect it first", name)
}

// DisconnectServer ends a server's SSH connection and holds it closed:
// nothing reconnects it until ConnectServer or ReconnectServer. Its
// projects show degraded and keep their forwards and sites, so connecting
// again brings them back as they were. Starting a project there meanwhile
// is refused.
func (e *Engine) DisconnectServer(name string) error {
	c, err := e.connOf(name, "this machine has no connection to drop")
	if err != nil {
		return err
	}
	c.ensure.Lock()
	defer c.ensure.Unlock()
	if err := e.stillCurrent(name, c); err != nil {
		return err
	}
	e.mu.Lock()
	e.held[name] = true
	e.mu.Unlock()
	e.serverLog(name, "disconnected, as asked")
	c.sess.Stop()
	return nil
}

// ConnectServer starts the connection of a server that was disconnected,
// or that nothing has connected to yet. The forwards it kept are added
// again once it is connected.
func (e *Engine) ConnectServer(name string) error {
	if name == config.LocalServer {
		return errors.New("this machine has no connection to make")
	}
	e.mu.Lock()
	srv, known := e.cfg.Server(name)
	c := e.servers[name]
	e.mu.Unlock()
	if !known {
		return fmt.Errorf("unknown server %q", name)
	}
	if c == nil {
		// A reload replaced the session it was dropped on, or nothing has
		// connected yet: start one.
		e.mu.Lock()
		delete(e.held, name)
		e.mu.Unlock()
		_, err := e.ensureSession(srv)
		return err
	}
	c.ensure.Lock()
	defer c.ensure.Unlock()
	if err := e.stillCurrent(name, c); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.held, name)
	e.mu.Unlock()
	e.serverLog(name, "connecting, as asked")
	c.sess.Start()
	return nil
}

// connOf is the connection of the server called name, for an action that
// needs one; local is the refusal for this machine.
func (e *Engine) connOf(name, local string) (*serverConn, error) {
	if name == config.LocalServer {
		return nil, errors.New(local)
	}
	e.mu.Lock()
	closed := e.closed
	_, known := e.cfg.Server(name)
	c := e.servers[name]
	e.mu.Unlock()
	switch {
	case closed:
		return nil, errShutDown
	case !known:
		return nil, fmt.Errorf("unknown server %q", name)
	case c == nil:
		return nil, fmt.Errorf("nothing is connected to %s yet", name)
	}
	return c, nil
}

// stillCurrent checks, under c's ensure lock, that the engine is open and
// c is still the server's connection: Shutdown stops each session under
// its ensure lock after marking the engine closed, and Apply may have
// dropped c while the caller waited for the lock.
func (e *Engine) stillCurrent(name string, c *serverConn) error {
	e.mu.Lock()
	closed := e.closed
	current := e.servers[name] == c
	e.mu.Unlock()
	switch {
	case closed:
		return errShutDown
	case !current:
		return fmt.Errorf("nothing is connected to %s yet", name)
	}
	return nil
}
