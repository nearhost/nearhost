package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// followsSession reports whether what run shows depends on its server's
// session: a running project, and one in error whose site is published all
// the same (a loopback port could not be forwarded when it started, say).
// Both have routes that are served only while the session carries them. So
// does a project whose start waits for the session to connect (run.waiting).
func (run *projectRun) followsSession() bool {
	return run.state == StateRunning || run.state == StateError && run.site || run.waiting
}

// waitingLocked is what a project whose start waits for its server shows:
// starting, with "waiting for <server>" and the session's last error, while
// the session tries; the error a start that met it has always shown while
// the session has given up (authentication failed); and starting alone once
// it has connected, as the start runs again. The message is the session's
// problem, titled with the text, when there is an error.
func (e *Engine) waitingLocked(run *projectRun) (State, string, *report.Message) {
	why := ""
	var problem *report.Message
	switch c := e.servers[run.server]; {
	case e.held[run.server]:
		why = run.server + " was disconnected on purpose"
	case c == nil:
	default:
		h := c.sess.Health()
		switch h.Status {
		case transport.StatusConnected:
			return StateStarting, "", nil
		case transport.StatusAuthFailed:
			msg := fmt.Sprintf("ssh %s: %s", c.sess.Host(), h.LastError)
			return StateError, msg, retitle(h.Problem, msg, report.Error)
		}
		why, problem = h.LastError, h.Problem
	}
	if why == "" {
		return StateStarting, "waiting for " + run.server, nil
	}
	msg := "waiting for " + run.server + ": " + why
	return StateStarting, msg, retitle(problem, msg, report.Warning)
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
// whatever it shows, and shows it alone again afterwards. A project whose
// start waits for its server shows what waitingLocked says.
//
// Beside the text it returns the problem as a message titled with it (see
// ProjectStatus.Problem), or nil when there is none. When several problems
// are joined in the text, the message has the code, fix and data of the
// first.
func (e *Engine) effectiveLocked(run *projectRun) (State, string, *report.Message) {
	if run.tearing && run.announced != "" {
		// Being torn down: the last announced state stands until the state
		// the teardown leads to is recorded.
		return run.announced, run.shownMsg, run.shownProblem
	}
	if !run.followsSession() {
		return run.state, run.message, run.problem
	}
	if run.waiting {
		return e.waitingLocked(run)
	}
	c := e.servers[run.server]
	if c == nil {
		return run.state, run.message, run.problem
	}
	status, states := c.sess.ForwardStates()
	if status != transport.StatusConnected {
		msg := fmt.Sprintf("server %s is %s", run.server, status)
		var problem *report.Message
		if e.held[run.server] {
			msg = fmt.Sprintf("server %s was disconnected on purpose", run.server)
		} else if h := c.sess.Health(); h.LastError != "" {
			msg += ": " + h.LastError
			problem = h.Problem
		}
		if run.state == StateError {
			msg += "; " + run.message
			if problem == nil {
				problem = run.problem
			}
		}
		return StateDegraded, msg, retitle(problem, msg, report.Warning)
	}
	dead := lostForwards(run, states)
	if len(dead) == 0 {
		return run.state, run.message, run.problem
	}
	for _, f := range dead {
		if held, reviewed := run.holders[f.local]; (!reviewed || held.reconnecting != f.reconnecting) && run.announced != "" {
			// Just lost, or lost another way: the session has changed but
			// its message has not reached reviewServerLocked yet. Until
			// that announces it, the last announced state stands, so
			// Status never runs ahead of the events.
			return run.announced, run.shownMsg, run.shownProblem
		}
	}
	// The session is connected, so its re-apply pass is over. A forward
	// being relaunched for a reason other than its port is reconnecting;
	// each other one was refused by ssh: the local port could not be bound.
	var refused, reconnecting []string
	var refusedProblem, reconnectingProblem *report.Message
	for _, f := range dead {
		web := slices.Contains(run.webLocals, f.local)
		if f.reconnecting {
			what := fmt.Sprintf("loopback port %d is reconnecting: %s", f.local, f.err)
			if web {
				what = fmt.Sprintf("the web route for port %d is reconnecting: %s", f.remote, f.err)
			}
			reconnecting = append(reconnecting, what)
			if reconnectingProblem == nil {
				reconnectingProblem = report.Warnf(report.ForwardReconnecting, "%s", what).
					Because("Nearhost tries again by itself.").
					With("port", f.local).With("remote_port", f.remote)
			}
			continue
		}
		what := fmt.Sprintf("loopback port %d is not forwarded: ssh could not bind it on this machine", f.local)
		if web {
			what = fmt.Sprintf("the web route for port %d is not forwarded: ssh could not bind local port %d", f.remote, f.local)
		}
		holder := run.holders[f.local].holder
		if holder != "" {
			what += " (in use by " + holder + ")"
		}
		refused = append(refused, what)
		if refusedProblem == nil {
			refusedProblem = report.Errorf(report.PortInUse, "%s", what).
				Then(fmt.Sprintf("Quit the program using port %d on this machine, then start the project again.", f.local)).
				With("port", f.local).With("remote_port", f.remote)
			if holder != "" {
				refusedProblem.With("holder", holder)
			}
		}
	}
	if len(refused) == 0 {
		msg := strings.Join(reconnecting, "; ")
		problem := reconnectingProblem
		if run.state == StateError {
			msg += "; " + run.message
		}
		return StateDegraded, msg, retitle(problem, msg, report.Warning)
	}
	problems := make([]string, 0, len(dead)+1)
	problem := refusedProblem
	if run.state == StateError {
		problems = append(problems, run.message)
		if run.problem != nil {
			problem = run.problem
		}
	}
	problems = append(problems, refused...)
	problems = append(problems, reconnecting...)
	msg := strings.Join(problems, "; ")
	return StateError, msg, retitle(problem, msg, report.Error)
}

// retitle is a copy of m titled title, at level l, for a reason that holds
// m's own title or joins it with others; nil when m is nil. An explanation
// the title already holds is dropped, so the message reads as its title
// says.
func retitle(m *report.Message, title string, l report.Level) *report.Message {
	if m == nil {
		return nil
	}
	c := copyMessage(m)
	c.Level, c.Title, c.Context = l, title, ""
	if c.Why != "" && strings.Contains(title, c.Why) {
		c.Why = ""
	}
	return c
}

// copyMessage is a copy of m that a caller may change without changing m;
// nil when m is nil.
func copyMessage(m *report.Message) *report.Message {
	if m == nil {
		return nil
	}
	c := *m
	c.Fix = slices.Clone(m.Fix)
	c.Data = maps.Clone(m.Data)
	return &c
}

// problemOf is the problem of a project that err put in its state: the
// message err holds, or an unexpected one, titled with err's text, which is
// the project's Message. It is nil when err is.
func problemOf(err error) *report.Message {
	if err == nil {
		return nil
	}
	m := report.From(err)
	m.Title, m.Context, m.Level = err.Error(), "", report.Error
	if m.Why != "" && strings.Contains(m.Title, m.Why) {
		m.Why = ""
	}
	if m.Cause == nil {
		m.Cause = err
	}
	return m
}

// lostForward is a forward of a project that its connected session lacks.
// It is reconnecting while the transport relaunches it for a reason other
// than its local port being refused, with err the reason; otherwise ssh
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
		reconnecting := st.Retrying && !st.PortRefused
		lost = append(lost, lostForward{forward: f, reconnecting: reconnecting, err: st.Err})
	}
	return lost
}

// changeLocked records a project's state, and err as the reason for it,
// and reports whether it changed. The caller announces it, with
// announceLocked or reviewServerLocked.
func (e *Engine) changeLocked(run *projectRun, st State, err error) bool {
	run.tearing = false // a teardown ends with the state it leads to
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if run.state == st && run.message == msg {
		return false
	}
	run.state, run.message, run.problem = st, msg, problemOf(err)
	run.announced = "" // an explicit change is always announced
	return true
}

// setStateLocked records a project's state and announces it. It announces
// also when the record did not change: this may be the end of a teardown,
// during which nothing was announced, and what the project shows can have
// changed with its server in the meantime. announceLocked publishes nothing
// when it has not.
func (e *Engine) setStateLocked(run *projectRun, st State, err error) {
	e.changeLocked(run, st, err)
	e.announceLocked(run)
}

// announceLocked publishes run's effective state if the state or its reason
// differs from what was published last, so the events always carry what
// Status reports. A reason only changes when the server's session changes
// status or a forward is lost or restored, so this is at most one event per
// message of the session. It is published under mu so events arrive in state
// order. The event's Report is the problem Status reports.
func (e *Engine) announceLocked(run *projectRun) {
	if run.tearing {
		return
	}
	run.shownWaiting = run.waiting
	st, msg, problem := e.effectiveLocked(run)
	if st == run.announced && msg == run.shownMsg {
		return
	}
	run.announced, run.shownMsg, run.shownProblem = st, msg, problem
	text := "state: " + string(st)
	if msg != "" {
		text += ": " + msg
	}
	ev := Event{Project: run.name, State: st, Message: text, Report: copyMessage(problem)}
	if run.server != config.LocalServer {
		ev.Server = run.server
	}
	e.bus.Publish(ev)
}

// logProject publishes a log line of a project.
func (e *Engine) logProject(run *projectRun, format string, args ...any) {
	e.publishProject(run, Event{Message: fmt.Sprintf(format, args...)})
}

// reportProject publishes m, a warning or an error of a project, as a log
// line of it whose text is m's title.
func (e *Engine) reportProject(run *projectRun, m *report.Message) {
	e.publishProject(run, Event{Message: m.Title, Report: m})
}

// publishProject publishes ev as a log line of run's project.
func (e *Engine) publishProject(run *projectRun, ev Event) {
	ev.Project = run.name
	if run.server != config.LocalServer {
		ev.Server = run.server
	}
	e.bus.Publish(ev)
}

// reasonError is the reason a project shows, as the error a start returns:
// its text, unwrapping to its problem so report.As finds that.
type reasonError struct {
	text    string
	problem *report.Message
}

func (e *reasonError) Error() string { return e.text }

// Unwrap is the problem.
func (e *reasonError) Unwrap() error { return e.problem }

// reasonErr is text as an error that carries problem, which may be nil.
func reasonErr(text string, problem *report.Message) error {
	if problem == nil {
		return errors.New(text)
	}
	return &reasonError{text: text, problem: problem}
}

// forwardProblem is err, a forward that failed, as a message: the message
// err holds, or forward.failed with err's text as its title.
func forwardProblem(err error) *report.Message {
	m := problemOf(err)
	if m.Code == report.Unexpected {
		m.Code = report.ForwardFailed
	}
	return m
}
