package engine

import (
	"fmt"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// A remote project's site is proxied to the local port of a forward. While
// the forward is not applied (its server's session reconnects, waits after
// refused logins or was disconnected, or the session refused to bind the
// port again), the port may be free, and any program could listen there
// and be sent the project's requests under its https name. So the proxy
// dials a forward only while the engine says it is up: each request passes
// the site's gate (proxy.Gate), which the engine opens and shuts with
// proxy.SetForwardUp under mu, as soon as it learns of a change. A shut
// gate answers the request itself, with 502 and, for a browser, a page
// saying Nearhost is reconnecting.
//
// The config does not change with the forward: a reload takes time, leaves
// the requests that arrive meanwhile with the old routes, and drops new
// connections on every site, so a session that flaps would disturb the
// others. A reload for another reason leaves the gate as it is.
//
// After an unexpected end of ssh, the engine learns of it from the
// session's Notify, which the session calls as soon as the process is
// reaped, before it reads why from ssh's stderr (a child of ssh, such as a
// ProxyJump, may hold that pipe for a while). The port is free from the
// moment ssh exited, so a request that passes the gate in between, the time
// it takes to reap the process and take mu, can still reach it. A teardown,
// a reconnect asked for and a disconnect shut the gates before they end
// anything, so they leave no such window.

// forwardAddr is the address the proxy dials the forward on local at.
func forwardAddr(local int) string {
	return fmt.Sprintf("127.0.0.1:%d", local)
}

// webUpLocked reports, for each of run's web routes, whether the proxy may
// dial its forward: the site is published and not being torn down, the
// server is neither held nor being reconnected on purpose, its session is
// connected, and the session has applied the forward, which it reports only
// once ssh listens on the local port. A local project has no forwards and
// its routes are always up.
func (e *Engine) webUpLocked(run *projectRun) []bool {
	up := make([]bool, len(run.webLocals))
	if run.server == config.LocalServer {
		for i := range up {
			up[i] = true
		}
		return up
	}
	c := e.servers[run.server]
	if !run.site || run.shut || e.held[run.server] || e.restarting[run.server] || c == nil {
		return up
	}
	status, states := c.sess.ForwardStates()
	if status != transport.StatusConnected {
		return up
	}
	applied := make(map[forward]bool, len(states))
	for _, st := range states {
		if st.Applied {
			applied[forward{st.Local, st.Remote}] = true
		}
	}
	recorded := make(map[forward]bool, len(run.forwards))
	for _, f := range run.forwards {
		recorded[f] = true
	}
	for i, w := range run.web {
		f := forward{run.webLocals[i], w.Port}
		up[i] = applied[f] && recorded[f]
	}
	return up
}

// syncGatesLocked opens and shuts the proxy's gates for run's web forwards
// as webUpLocked says. A local project has none.
func (e *Engine) syncGatesLocked(run *projectRun) {
	if run.server == config.LocalServer {
		return
	}
	for i, up := range e.webUpLocked(run) {
		proxy.SetForwardUp(forwardAddr(run.webLocals[i]), up)
	}
}
