package engine

import (
	"errors"
	"fmt"

	"github.com/nearhost/nearhost/internal/config"
)

// ReconnectServer ends the SSH connection of a server and starts it again.
// The session keeps the forwards it was given and adds them again once it
// is connected, so the server's projects show degraded for a moment and
// then return to what they were.
func (e *Engine) ReconnectServer(name string) error {
	if name == config.LocalServer {
		return errors.New("this machine has no connection to reconnect")
	}
	e.mu.Lock()
	closed := e.closed
	_, known := e.cfg.Server(name)
	c := e.servers[name]
	e.mu.Unlock()
	switch {
	case closed:
		return errShutDown
	case !known:
		return fmt.Errorf("unknown server %q", name)
	case c == nil:
		return fmt.Errorf("nothing is connected to %s yet", name)
	}
	c.ensure.Lock()
	defer c.ensure.Unlock()
	// Shutdown stops each session under its ensure lock after marking the
	// engine closed, so a session is never started behind its back.
	e.mu.Lock()
	closed = e.closed
	e.mu.Unlock()
	if closed {
		return errShutDown
	}
	e.serverLog(name, "reconnecting, as asked")
	c.sess.Stop()
	c.sess.Start()
	return nil
}
