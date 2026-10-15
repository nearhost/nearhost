package engine

// ReconnectServer ends the SSH connection of a server and starts it again.
// The session keeps the forwards it was given and adds them again once it
// is connected, so the server's projects show degraded for a moment and
// then return to what they were. A server that was disconnected on purpose
// is connected again.
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
	e.mu.Lock()
	delete(e.held, name)
	e.mu.Unlock()
	e.serverLog(name, "reconnecting, as asked")
	c.sess.Stop()
	c.sess.Start()
	return nil
}
