package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/transport"
)

// cleanupTimeout bounds undoing a start that failed or was interrupted.
const cleanupTimeout = 15 * time.Second

// webPortAttempts is how many local ports are tried for a web forward.
const webPortAttempts = 3

type forward struct{ local, remote int }

// projectRun is the runtime state of one project. name, alias and server
// never change; everything below op is guarded by Engine.mu.
type projectRun struct {
	name, alias, server string

	op sync.Mutex // serialises starting and stopping this project

	state     State
	message   string
	announced State              // effective state last published
	shownMsg  string             // its message
	tearing   bool               // a teardown has begun; cleared by the state change that follows it
	web       []config.WebHosts  // the web routes of the published site and their hosts; nil when none
	webLocals []int              // local port the proxy sends each of web to, aligned with it; nil when none
	site      bool               // the site is published to the proxy
	forwards  []forward          // forwards this project added to its server's session
	cancel    context.CancelFunc // interrupts a start in progress; nil otherwise
	gone      bool               // Apply replaced or removed the project; this run is never started again
	holders   map[int]heldPort   // local port of each lost forward that was announced -> what is known of its holder
}

// heldPort is what is known about the local port of a lost forward.
type heldPort struct {
	lookup uint64 // the lookup that may still name the holder
	holder string // the process holding the port; empty while not known
}

// StartProject brings a project up: it connects the project's server, makes
// its alias and hosts resolve, adds its forwards and publishes its site, one
// proxy site per web route.
//
// A local project needs no forwards: the proxy sends each web route's hosts
// straight to its port and loopback routes are already local. For a remote
// project each web route is forwarded to a free local port of its own and
// each loopback route to the same port number on this machine.
//
// Starting a project that is running or already starting is a no-op that
// returns nil at once; it never adds a second set of forwards. The one
// exception: when the project's server gave up connecting (authentication
// failed), the connection is tried again, and the project leaves the degraded
// state once it is back. Starting a project in the error state first removes
// whatever its last attempt left; that includes a running project that lost
// a forward. Starting a project that is being stopped waits for the stop and
// then starts it.
//
// Outcomes other than running, each returned as an error as well:
//   - the server cannot be reached, a web forward fails, a host is already
//     served by another running project, or the proxy rejects the site:
//     state error, nothing left running for the project;
//   - a loopback port is in use on this machine or held by another project:
//     state error naming the port, with the web routes and the other
//     loopback routes still served;
//   - the hosts file could not be updated: state stopped with a message
//     saying the alias is not resolvable, nothing started for the project.
//
// If the server's connection drops while the forwards are being added, they
// are queued on the session and StartProject returns nil with the project
// showing degraded. When the session reconnects the project becomes running,
// or error naming the port of a forward that could not be applied.
//
// The same holds later on for a running project, and for one that started in
// error with its web routes served: while the server is away it shows
// degraded, and after every reconnect a forward the new master refused puts
// the project in error naming the port, with its other routes still served,
// until a later reconnect or a new start restores the forward. The error a
// project started with is kept in each of these messages. The process holding the port is looked up in the
// background and added to the message, in a second announcement, when it is
// found.
//
// A failure never changes another project.
func (e *Engine) StartProject(ctx context.Context, name string) error {
	e.mu.Lock()
	cfg := e.cfg
	run := e.runs[name]
	p, ok := cfg.Project(name)
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unknown project %q", name)
	}
	if run.gone {
		// Apply is replacing the project. Checked before the busy path,
		// which could otherwise revive the server's session with the
		// configuration this run came from.
		e.mu.Unlock()
		return errChanged(name)
	}
	busy := e.busyLocked(run)
	c := e.servers[run.server]
	e.mu.Unlock()
	if busy {
		// Nothing to add. The one thing worth doing for a running project is
		// reviving a server connection that gave up; the session re-applies
		// the project's forwards when it connects.
		if srv, ok := cfg.Server(p.Server); ok && c != nil && c.sess.Status() == transport.StatusAuthFailed {
			_, err := e.ensureSession(srv)
			return err
		}
		return nil
	}
	run.op.Lock()
	defer run.op.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	switch {
	case e.closed:
		e.mu.Unlock()
		return errShutDown
	case !e.started:
		e.mu.Unlock()
		return errors.New("engine is not started")
	case run.gone: // Apply changed the project while this start waited
		e.mu.Unlock()
		return errChanged(name)
	case e.busyLocked(run): // another start finished while this one waited
		e.mu.Unlock()
		return nil
	}
	run.cancel = cancel
	e.setStateLocked(run, StateStarting, "")
	e.mu.Unlock()

	st, err := e.bringUp(ctx, cfg, p, run)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run.cancel = nil
	// Announce through the review: the forwards may have been queued on a
	// session that dropped, or lost again since.
	e.changeLocked(run, st, msg)
	e.reviewServerLocked(run.server)
	if err == nil {
		if shown, why := e.effectiveLocked(run); shown == StateError {
			err = errors.New(why)
		}
	}
	return err
}

// errChanged is the error of a start on a project Apply replaced.
func errChanged(name string) error {
	return fmt.Errorf("project %q changed while it was waiting to start; start it again", name)
}

// busyLocked reports whether a start has nothing to do: the project is
// starting, or running with nothing lost. A running project that shows an
// error because a forward is gone is not busy; starting it replaces its
// forwards. Nor is one that is being stopped, although it still shows its
// last state: the start waits for the stop (on op) and runs after it.
func (e *Engine) busyLocked(run *projectRun) bool {
	switch run.state {
	case StateStarting:
		return true
	case StateRunning:
		if run.tearing {
			return false // only a stop tears down a running project
		}
		shown, _ := e.effectiveLocked(run)
		return shown != StateError
	}
	return false
}

// bringUp does the work of StartProject and returns the state the project
// ends in. cfg is the configuration p comes from. The caller holds run.op.
func (e *Engine) bringUp(ctx context.Context, cfg *config.Config, p *config.Project, run *projectRun) (State, error) {
	web := p.Web()
	if len(web) == 0 {
		return StateError, fmt.Errorf("project %q has no web route", p.Name)
	}
	srv, ok := cfg.Server(p.Server)
	if !ok {
		return StateError, fmt.Errorf("project %q uses unknown server %q", p.Name, p.Server)
	}
	if err := config.ValidProjectAlias(p.Alias); err != nil {
		return StateError, fmt.Errorf("project %q: %w", p.Name, err)
	}
	// Leftovers of a start that ended in error go first, so their forwards
	// are cancelled rather than added to.
	if err := e.tearDown(ctx, run); err != nil {
		return StateError, fmt.Errorf("remove previous start: %w", err)
	}

	// Refused before anything is set up, so a doomed start neither connects
	// nor asks for a password. publishSite checks again, as another project
	// may take the host in the meantime.
	if err := e.hostClash(run, web); err != nil {
		return StateError, err
	}

	var sess *transport.Session
	if p.Server != config.LocalServer {
		var err error
		if sess, err = e.ensureSession(srv); err != nil {
			return StateError, err
		}
		if err := e.waitConnected(ctx, sess); err != nil {
			return StateError, err
		}
	}
	if err := ctx.Err(); err != nil {
		return StateError, err
	}
	if err := e.ensureHosts(ctx); err != nil {
		return StateStopped, fmt.Errorf("alias %s is not resolvable: the hosts file was not updated (%w); start the project again to retry", p.Alias, err)
	}

	webLocals := make([]int, len(web))
	for i, w := range web {
		webLocals[i] = w.Port
	}
	var problems []string
	if sess != nil {
		for i, w := range web {
			local, err := e.forwardWeb(ctx, sess, run, w.Port)
			if err != nil {
				// The web routes forwarded so far go with the rest.
				return StateError, errors.Join(fmt.Errorf("web port %d: %w", w.Port, err), e.rollBack(ctx, run))
			}
			webLocals[i] = local
		}
		problems = e.forwardLoopbacks(ctx, sess, run, p)
	}
	err := ctx.Err()
	if err == nil {
		if err = e.publishSite(run, web, webLocals); err == nil {
			// Interrupted while the proxy took the site: the start did not
			// happen, and the site goes again with the rest.
			err = ctx.Err()
		}
	}
	if err != nil {
		return StateError, errors.Join(err, e.rollBack(ctx, run))
	}
	if len(problems) > 0 {
		return StateError, errors.New(strings.Join(problems, "; "))
	}
	return StateRunning, nil
}

// waitConnected waits for the server's session, for at most connectWait.
func (e *Engine) waitConnected(ctx context.Context, sess *transport.Session) error {
	wctx, cancel := context.WithTimeout(ctx, e.connectWait)
	defer cancel()
	err := sess.WaitConnected(wctx)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("ssh %s: not connected after %s", sess.Host, e.connectWait)
		if last := sess.LastError(); last != "" {
			err = fmt.Errorf("%w: %s", err, last)
		}
	}
	return err
}

// ensureHosts asks for every hosts file name of the configuration (see
// config.HostsFileNames) to resolve, leaving out the names DNS already sends
// to this machine (see hosts.Wanted). Calls are serialised so
// two starting projects never prompt for a password at the same time. Both
// the wait for the turn and the update itself end when ctx does.
func (e *Engine) ensureHosts(ctx context.Context) error {
	if e.opts.EnsureHosts == nil {
		return nil
	}
	select {
	case e.hostsTurn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-e.hostsTurn }()
	if err := ctx.Err(); err != nil {
		return err // interrupted just as its turn came
	}
	e.mu.Lock()
	names := append([]string(nil), e.hostNames...)
	e.mu.Unlock()
	return e.opts.EnsureHosts(ctx, hosts.Wanted(ctx, names, e.opts.Lookup))
}

// claimPort reserves a local port for a forward of run. It returns the name
// of the project holding the port when another one does.
func (e *Engine) claimPort(run *projectRun, port int) (owner string, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if owner, taken := e.ports[port]; taken && owner != run.name {
		return owner, false
	}
	e.ports[port] = run.name
	return "", true
}

func (e *Engine) releasePort(port int) {
	e.mu.Lock()
	delete(e.ports, port)
	e.mu.Unlock()
}

// forwardWeb forwards a web route's remote port to a free local port chosen
// here and returns that port. The forward is recorded on run, so a rollback
// cancels it; the proxy learns the port when the site is published.
func (e *Engine) forwardWeb(ctx context.Context, sess *transport.Session, run *projectRun, remote int) (int, error) {
	var last error
	for attempt := 0; attempt < webPortAttempts && ctx.Err() == nil; attempt++ {
		local, err := transport.FreePort()
		if err != nil {
			return 0, err
		}
		if _, ok := e.claimPort(run, local); !ok {
			last = fmt.Errorf("local port %d is held by another project", local)
			continue
		}
		if err := sess.AddForward(ctx, local, remote); err != nil {
			e.releasePort(local)
			last = err
			continue
		}
		e.mu.Lock()
		run.forwards = append(run.forwards, forward{local, remote})
		e.mu.Unlock()
		return local, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, last
}

// forwardLoopbacks forwards each loopback route to the same port on this
// machine. A route that cannot be forwarded is reported, logged for the
// project and skipped; the others still start.
func (e *Engine) forwardLoopbacks(ctx context.Context, sess *transport.Session, run *projectRun, p *config.Project) []string {
	var problems []string
	done := map[int]bool{}
	for _, r := range p.Routes {
		if r.Role != config.RoleLoopback || done[r.Port] {
			continue
		}
		done[r.Port] = true
		if err := e.forwardLoopback(ctx, sess, run, r.Port); err != nil {
			problems = append(problems, err.Error())
			e.logProject(run, "%v", err)
		}
	}
	return problems
}

func (e *Engine) forwardLoopback(ctx context.Context, sess *transport.Session, run *projectRun, port int) error {
	// Two projects may not share a local port: the session would treat the
	// second forward as the first, and stopping either would cancel it.
	if owner, ok := e.claimPort(run, port); !ok {
		return fmt.Errorf("loopback port %d is already forwarded for project %q", port, owner)
	}
	if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		e.releasePort(port)
		return fmt.Errorf("loopback port %d is already in use on this machine%s: %w", port, e.heldBy(ctx, port), err)
	} else if err := l.Close(); err != nil {
		e.releasePort(port)
		return fmt.Errorf("loopback port %d: %w", port, err)
	}
	if err := sess.AddForward(ctx, port, port); err != nil {
		e.releasePort(port)
		return fmt.Errorf("loopback port %d: %w", port, err)
	}
	e.mu.Lock()
	run.forwards = append(run.forwards, forward{port, port})
	e.mu.Unlock()
	return nil
}

// publishSite adds run's site to the proxy: each of web, sent to the local
// port at the same index of webLocals. Other sites keep serving. It refuses a
// host another published site serves, checked under applyMu so two starts
// cannot both take it.
func (e *Engine) publishSite(run *projectRun, web []config.WebHosts, webLocals []int) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	if err := e.hostClashLocked(run, web); err != nil {
		e.mu.Unlock()
		return err
	}
	run.web, run.webLocals, run.site = web, webLocals, true
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if err := e.applySites(sites); err != nil {
		// The proxy keeps its previous configuration, which lacks this site.
		e.mu.Lock()
		run.web, run.webLocals, run.site = nil, nil, false
		e.mu.Unlock()
		// The proxy's errors already say they are the proxy's.
		return err
	}
	return nil
}

// hostClash is hostClashLocked for a caller that does not hold mu.
func (e *Engine) hostClash(run *projectRun, web []config.WebHosts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hostClashLocked(run, web)
}

// hostClashLocked refuses a host of web that another project's published site
// already serves. Hosts are compared ignoring case, so two exact hosts clash
// when they are the same name and two patterns when written the same. An
// exact host under another project's pattern does not clash: the proxy sends
// it to the more specific match, as config.HostClash allows.
func (e *Engine) hostClashLocked(run *projectRun, web []config.WebHosts) error {
	taken := map[string]string{}
	for _, name := range e.order {
		other := e.runs[name]
		if other == run || !other.site {
			continue
		}
		for _, w := range other.web {
			for _, h := range w.Hosts {
				if _, seen := taken[strings.ToLower(h)]; !seen {
					taken[strings.ToLower(h)] = other.name
				}
			}
		}
	}
	for _, w := range web {
		for _, h := range w.Hosts {
			if owner, ok := taken[strings.ToLower(h)]; ok {
				return fmt.Errorf("host %s is already served by project %q", h, owner)
			}
		}
	}
	return nil
}
