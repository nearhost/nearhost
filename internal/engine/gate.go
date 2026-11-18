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
// dials a forward only while the engine says it is up, at two levels:
//
//   - each request passes the site's gate (proxy.Gate), which the engine
//     opens and shuts with proxy.SetForwardUp under mu, as soon as it
//     learns of a change, with no config reload;
//   - the site is published Down, with no upstream at all, while the
//     forward is not up, and with its upstream again once it is.
//
// The gate is what keeps the port from being dialled once it is free:
// a reload takes time, and requests that arrive meanwhile have the old
// routes. The engine learns that a session dropped from the session's own
// log line, which it writes as soon as it finds its ssh gone; the port is
// free from the moment ssh exited, so a request that passes the gate in
// between, a matter of the time it takes to reap the process, can still
// reach it. A teardown, a reconnect asked for and a disconnect shut the
// gates before they end anything, so they leave no such window.

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
// as webUpLocked says, and reports whether any changed. A local project has
// none.
func (e *Engine) syncGatesLocked(run *projectRun) bool {
	if run.server == config.LocalServer {
		return false
	}
	changed := false
	for i, up := range e.webUpLocked(run) {
		if proxy.SetForwardUp(forwardAddr(run.webLocals[i]), up) {
			changed = true
		}
	}
	return changed
}

// republishLocked has the proxy given the sites again, by a goroutine of its
// own, as a forward of a published site went down or came back: the caller
// may hold a session's forward lock, and an apply waits for applyMu. Calls
// made while one runs are coalesced into one more apply after it, so a
// session that flaps costs at most one reload under way and one waiting,
// and each apply has the state of the moment it begins. Caddy skips a
// config that has not changed, and lets the requests of other sites that are
// under way finish on the routes they began with.
func (e *Engine) republishLocked() {
	if e.closed {
		return // Shutdown stops every project, which removes the sites
	}
	if e.republishing {
		e.republishAgain = true
		return
	}
	e.republishing = true
	e.siteWork.Add(1) // under mu while closed is not set, so before Shutdown waits
	go e.republish()
}

// republish gives the proxy the sites until no change is left that it has
// not applied. It runs on its own goroutine, counted in siteWork.
func (e *Engine) republish() {
	defer e.siteWork.Done()
	for {
		e.applyMu.Lock()
		e.mu.Lock()
		e.republishAgain = false
		sites := e.sitesLocked(nil)
		e.mu.Unlock()
		err := e.refreshSites(sites)
		e.applyMu.Unlock()
		if err != nil {
			e.bus.Publish(Event{Message: fmt.Sprintf("could not update the proxy's sites: %v", err)})
		}
		e.mu.Lock()
		if !e.republishAgain || e.closed {
			e.republishing = false
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
	}
}
