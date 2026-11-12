package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

// followsSession reports whether what run shows depends on its server's
// session: a running project, and one in error whose site is published all
// the same (a loopback port could not be forwarded when it started, say).
// Both have routes that are served only while the session carries them.
func (run *projectRun) followsSession() bool {
	return run.state == StateRunning || run.state == StateError && run.site
}

// effectiveLocked is the state to show for run. It is the recorded state,
// except for a project on a remote server that follows its session (see
// followsSession): that is degraded while the server's session is not
// connected, and in error while the session is connected but lacks one of
// the project's forwards, or degraded again when each forward it lacks is
// one the transport is relaunching by itself (see lostForward). These are
// derived from the session each time, so Status and the events always
// agree, and they end by themselves when a reconnect restores what was
// missing. The error names the process holding a port once lookupHolder has
// found it. A project recorded in error keeps the reason for that in
// whatever it shows, and shows it alone again afterwards.
func (e *Engine) effectiveLocked(run *projectRun) (State, string) {
	if run.tearing && run.announced != "" {
		// Being torn down: the last announced state stands until the state
		// the teardown leads to is recorded.
		return run.announced, run.shownMsg
	}
	if !run.followsSession() {
		return run.state, run.message
	}
	c := e.servers[run.server]
	if c == nil {
		return run.state, run.message
	}
	status, states := c.sess.ForwardStates()
	if status != transport.StatusConnected {
		msg := fmt.Sprintf("server %s is %s", run.server, status)
		if e.held[run.server] {
			msg = fmt.Sprintf("server %s was disconnected on purpose", run.server)
		} else if last := c.sess.LastError(); last != "" {
			msg += ": " + last
		}
		if run.state == StateError {
			msg += "; " + run.message
		}
		return StateDegraded, msg
	}
	dead := lostForwards(run, states)
	if len(dead) == 0 {
		return run.state, run.message
	}
	for _, f := range dead {
		if held, reviewed := run.holders[f.local]; (!reviewed || held.reconnecting != f.reconnecting) && run.announced != "" {
			// Just lost, or lost another way: the session has changed but
			// its message has not reached reviewServerLocked yet. Until
			// that announces it, the last announced state stands, so
			// Status never runs ahead of the events.
			return run.announced, run.shownMsg
		}
	}
	// The session is connected, so its re-apply pass is over. A forward
	// being relaunched for a reason other than its port is reconnecting;
	// each other one was refused by ssh: the local port could not be bound.
	var refused, reconnecting []string
	for _, f := range dead {
		web := slices.Contains(run.webLocals, f.local)
		if f.reconnecting {
			what := fmt.Sprintf("loopback port %d is reconnecting: %s", f.local, f.err)
			if web {
				what = fmt.Sprintf("the web route for port %d is reconnecting: %s", f.remote, f.err)
			}
			reconnecting = append(reconnecting, what)
			continue
		}
		what := fmt.Sprintf("loopback port %d is not forwarded: ssh could not bind it on this machine", f.local)
		if web {
			what = fmt.Sprintf("the web route for port %d is not forwarded: ssh could not bind local port %d", f.remote, f.local)
		}
		if holder := run.holders[f.local].holder; holder != "" {
			what += " (in use by " + holder + ")"
		}
		refused = append(refused, what)
	}
	if len(refused) == 0 {
		msg := strings.Join(reconnecting, "; ")
		if run.state == StateError {
			msg += "; " + run.message
		}
		return StateDegraded, msg
	}
	problems := make([]string, 0, len(dead)+1)
	if run.state == StateError {
		problems = append(problems, run.message)
	}
	problems = append(problems, refused...)
	problems = append(problems, reconnecting...)
	return StateError, strings.Join(problems, "; ")
}

// lostForward is a forward of a project that its connected session lacks.
// It is reconnecting while the transport relaunches it for a reason other
// than its local port being in use, with err the reason; otherwise ssh
// could not bind its local port.
type lostForward struct {
	forward
	reconnecting bool
	err          string
}

// lostForwards lists the forwards of run that are missing from states or not
// applied there.
func lostForwards(run *projectRun, states []transport.ForwardState) []lostForward {
	byForward := make(map[forward]transport.ForwardState, len(states))
	for _, st := range states {
		byForward[forward{st.Local, st.Remote}] = st
	}
	var lost []lostForward
	for _, f := range run.forwards {
		st := byForward[f]
		if st.Applied {
			continue
		}
		reconnecting := st.Retrying && !strings.Contains(st.Err, "Address already in use")
		lost = append(lost, lostForward{forward: f, reconnecting: reconnecting, err: st.Err})
	}
	return lost
}

// changeLocked records a project's state and reports whether it changed. The
// caller announces it, with announceLocked or reviewServerLocked.
func (e *Engine) changeLocked(run *projectRun, st State, msg string) bool {
	run.tearing = false // a teardown ends with the state it leads to
	if run.state == st && run.message == msg {
		return false
	}
	run.state, run.message = st, msg
	run.announced = "" // an explicit change is always announced
	return true
}

// setStateLocked records a project's state and announces it. It announces
// also when the record did not change: this may be the end of a teardown,
// during which nothing was announced, and what the project shows can have
// changed with its server in the meantime. announceLocked publishes nothing
// when it has not.
func (e *Engine) setStateLocked(run *projectRun, st State, msg string) {
	e.changeLocked(run, st, msg)
	e.announceLocked(run)
}

// announceLocked publishes run's effective state if the state or its reason
// differs from what was published last, so the events always carry what
// Status reports. A reason only changes when the server's session changes
// status or a forward is lost or restored, so this is at most one event per
// message of the session. It is published under mu so events arrive in state
// order.
func (e *Engine) announceLocked(run *projectRun) {
	if run.tearing {
		return
	}
	st, msg := e.effectiveLocked(run)
	if st == run.announced && msg == run.shownMsg {
		return
	}
	run.announced, run.shownMsg = st, msg
	text := "state: " + string(st)
	if msg != "" {
		text += ": " + msg
	}
	ev := Event{Project: run.name, State: st, Message: text}
	if run.server != config.LocalServer {
		ev.Server = run.server
	}
	e.bus.Publish(ev)
}

// logProject publishes a log line of a project.
func (e *Engine) logProject(run *projectRun, format string, args ...any) {
	ev := Event{Project: run.name, Message: fmt.Sprintf(format, args...)}
	if run.server != config.LocalServer {
		ev.Server = run.server
	}
	e.bus.Publish(ev)
}
