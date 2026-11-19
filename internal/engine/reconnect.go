package engine

import "github.com/nearhost/nearhost/internal/transport"

// ReconnectServer ends the SSH connection of a server and starts it again.
// The session keeps the forwards it was given and adds them again once it
// is connected, so the server's projects show degraded for a moment and
// then return to what they were. A server that was disconnected on purpose
// is connected again. A server that refused the logins is tried once, as
// the session's Start does.
func (e *Engine) ReconnectServer(name string) error {
	c, err := e.connOf(name, "this machine has no connection to reconnect")
	if err != nil {
		return err
	}
	c.ensure.Lock()
	defer c.ensure.Unlock()
	// Apply may have dropped this session while the reconnect waited for
	// it: starting it again would leave it running, unknown to the engine.
	if err := e.stillCurrent(name, c); err != nil {
		return err
	}
	// The server refused the logins: Start makes the session try once at
	// once, or leaves an attempt under way to answer for this one too, so
	// pressing r again and again cannot spend a refusal each time.
	h := c.sess.Health()
	refused := h.Refused && h.Status != transport.StatusConnected
	e.mu.Lock()
	delete(e.held, name)
	if !refused {
		// Stop frees the forwards' ports before the session says it is
		// disconnected: the sites dial nothing from the log line below.
		e.restarting[name] = true
	}
	e.mu.Unlock()
	e.serverLog(name, "reconnecting, as asked")
	if refused {
		c.sess.Start()
		return nil
	}
	c.sess.Stop()
	e.mu.Lock()
	delete(e.restarting, name) // the session is disconnected now
	e.mu.Unlock()
	c.sess.Start()
	return nil
}
