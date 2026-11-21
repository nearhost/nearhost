package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/oserr"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// cleanupTimeout bounds undoing a start that failed or was interrupted.
const cleanupTimeout = 15 * time.Second

// webPortAttempts is how many local ports are tried for a web forward.
const webPortAttempts = 3

// webPortSkips is how many ports offered for a web forward may be passed
// over, as kept for another route, before the forward fails. They do not
// count as attempts.
const webPortSkips = 100

type forward struct{ local, remote int }

// projectRun is the runtime state of one project. name and server never
// change. alias changes only when a start adopts a main name its .nearhostrc
// moved without needing approval (adoptLocked), under Engine.mu, so it is
// read under mu, as everything below op is guarded by Engine.mu.
type projectRun struct {
	name, server string
	alias        string

	op sync.Mutex // serialises starting and stopping this project

	state     State
	message   string
	announced State              // effective state last published
	shownMsg  string             // its message
	tearing   bool               // a teardown has begun; cleared by the state change that follows it
	web       []config.WebHosts  // the web routes of the published site and their hosts; nil when none
	webLocals []int              // local port the proxy sends each of web to, aligned with it; nil when none
	site      bool               // the site is published to the proxy
	docker    bool               // the published site is served to the Docker networks too (docker_clients)
	forwards  []forward          // forwards this project added to its server's session
	cancel    context.CancelFunc // interrupts a start in progress; nil otherwise
	gone      bool               // Apply replaced or removed the project; this run is never started again
	holders   map[int]heldPort   // local port of each lost forward that was announced -> what is known of its holder
	pending   *Change            // what the project's .nearhostrc asks for that waits for approval; nil when nothing does
	notice    string             // why the .nearhostrc could not be taken into account; empty when it could
	declined  string             // hash of a .nearhostrc whose change the user declined; empty when none
	// waiting is set while a start whose server did not connect in time
	// waits on for it: the project is recorded starting and shows what
	// waitingLocked says, until the session connects and the start runs
	// again (resumeStart). A stop or a start that takes over clears it.
	waiting bool
	// resuming is set once the session of a waiting project has connected
	// and its start has been handed to resumeStart; that start runs only
	// while it is still set.
	resuming bool
	// shownWaiting is waiting as it was at the last announcement, so Status
	// reports the server waited for alongside the state it shows, also while
	// a teardown holds that state.
	shownWaiting bool
	// stopping is set while StopProject runs, from its first look at the
	// project to the state it records.
	stopping bool
	// shut is set by a teardown, before it removes the site or cancels a
	// forward, and cleared when the site is published again: until then
	// the proxy dials none of the project's forwards (see webUpLocked).
	shut bool
}

// heldPort is what is known about the local port of a lost forward.
type heldPort struct {
	lookup uint64 // the lookup that may still name the holder; 0 for none
	holder string // the process holding the port; empty while not known
	// reconnecting is set while the forward is being relaunched for a
	// reason other than its port: nobody is asked who holds the port then.
	reconnecting bool
}

// StartProject brings a project up: it connects the project's server, makes
// its alias and hosts resolve, adds its forwards and publishes its site, one
// proxy site per web route.
//
// With Options.Review set, the project's .nearhostrc is read again once its
// server is connected, and the start goes on with the project as the review
// leaves it: a change that needed no approval is adopted (see Review), while
// one that waits for approval, or a file that cannot be read, leaves the
// approved routes serving and shows in the project's status.
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
//   - the server's session cannot be started, a web forward fails, a host
//     is already served by another running project, or the proxy rejects
//     the site: state error, nothing left running for the project;
//   - a loopback port is in use on this machine or held by another project:
//     state error naming the port, with the web routes and the other
//     loopback routes still served;
//   - the hosts file could not be updated: state stopped with a message
//     saying the project's hosts are not resolvable, nothing started for
//     the project.
//
// When the server does not connect within connectWait, or gives up meanwhile
// (authentication failed), the start waits on for it and StartProject returns
// what the project shows. The project stays starting, with the message
// "waiting for <server>: <the session's last error>", or shows the error of a
// session that gave up, as a start that met it always has. Once the session
// connects, whatever reconnected it, the start runs again by itself. Stopping
// the project, or shutting down, drops the waiting start. Starting it again
// meanwhile replaces the waiting start with a new one, which tries a
// connection that gave up again, as for a project in error.
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
	return e.startProject(ctx, name, false)
}

// resumeStart runs the start of a project that waited for its server, once
// the server's session has connected. reviewServerLocked hands it over, on a
// goroutine counted in starts.
func (e *Engine) resumeStart(name string) {
	defer e.starts.Done()
	_ = e.startProject(e.ctx, name, true) // the outcome is the project's state
}

// startProject is StartProject. With resume set it is the start a waiting
// project hands to resumeStart: it goes ahead only while the project still
// waits for it (run.resuming), which a stop or a start that took over clears.
func (e *Engine) startProject(ctx context.Context, name string, resume bool) error {
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
	if e.startQueued != nil {
		e.startQueued(name)
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
	case resume && !run.resuming: // stopped, or started anew, meanwhile
		e.mu.Unlock()
		return nil
	case e.busyLocked(run): // another start finished while this one waited
		e.mu.Unlock()
		return nil
	}
	run.cancel = cancel
	run.waiting, run.resuming = false, false
	e.setStateLocked(run, StateStarting, "")
	e.mu.Unlock()

	st, err := e.bringUp(ctx, cfg, p, run)
	if st == StateStarting && e.beforeWaiting != nil {
		e.beforeWaiting(ctx, name)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run.cancel = nil
	if st == StateStarting && ctx.Err() != nil {
		// Stopped, or shut down, since the wait ran out: the stop cancelled
		// ctx under mu before this, and found nothing waiting to drop, so
		// nothing may wait now. The stop records the state it leads to.
		st, err = StateError, ctx.Err()
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if st == StateStarting {
		// The server did not connect in time: the start waits on for it.
		// The review announces what that shows, and hands the start over
		// at once if the session connected since the wait ran out.
		run.waiting = true
		e.changeLocked(run, StateStarting, "")
		e.reviewServerLocked(run.server)
		if _, why := e.effectiveLocked(run); why != "" {
			return errors.New(why)
		}
		return fmt.Errorf("waiting for %s", run.server)
	}
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
// starting, or running with nothing lost. A project whose start waits for its
// server (run.waiting) is not busy: starting it again starts it afresh, and
// retries a connection that gave up, as for a project in error. A running
// project that shows an error because a forward is gone is not busy; starting
// it replaces its forwards. Nor is a starting or running project that is
// being stopped (run.stopping, run.tearing), although it still shows its last
// state: the start queues on op and runs when it gets it. A stop holds op from
// the moment it tears down, and from its first step when the project waits
// for its server, so a start made after that runs after the stop.
func (e *Engine) busyLocked(run *projectRun) bool {
	switch run.state {
	case StateStarting:
		return !run.waiting && !run.stopping
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
// StateStarting means the server did not connect in time, or gave up, while
// ctx went on: nothing is left of the start, which waits on for the server.
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

	var sess transport.Transport
	if p.Server != config.LocalServer {
		// Refused before connecting, so a start that is doomed by the hosts
		// already approved asks for no password. It is checked again below
		// with the hosts the project's .nearhostrc leaves, and publishSite
		// checks once more, as another project may take a host meanwhile.
		if err := e.hostClash(run, web); err != nil {
			return StateError, err
		}
		var err error
		if sess, err = e.ensureSession(srv); err != nil {
			return StateError, err
		}
		if err := e.waitConnected(ctx, sess); err != nil {
			if ctx.Err() == nil {
				return StateStarting, err
			}
			return StateError, err
		}
	}
	if err := ctx.Err(); err != nil {
		return StateError, err
	}
	// Everything from here on uses the project as its .nearhostrc leaves it.
	p = e.reviewStart(ctx, run, p, sess)
	if err := ctx.Err(); err != nil {
		return StateError, err
	}
	if web = p.Web(); len(web) == 0 {
		return StateError, fmt.Errorf("project %q has no web route", p.Name)
	}
	// The review may have moved the main name, so it is checked again, as
	// are the clashes and the hosts file below, with the name now served.
	if err := config.ValidProjectAlias(p.Alias); err != nil {
		return StateError, fmt.Errorf("project %q: %w", p.Name, err)
	}
	if err := e.hostClash(run, web); err != nil {
		return StateError, err
	}
	if err := e.ensureHosts(ctx, web); err != nil {
		return StateStopped, fmt.Errorf("project %q's hosts are not resolvable: the hosts file was not updated (%w); start the project again to retry", p.Name, err)
	}
	// Validate refuses docker_clients on a remote project; checking the
	// server here too means a config that skipped it still never serves a
	// remote project to the local Docker networks.
	docker := p.DockerClients() && p.Server == config.LocalServer
	if docker {
		// Asked before the site is published, so it is never served to
		// fewer networks than Docker has, nor to more than it needs.
		if err := e.dockerStart(ctx, run); err != nil {
			return StateError, err
		}
	}

	webLocals := make([]int, len(web))
	for i, w := range web {
		webLocals[i] = w.Port
	}
	var problems []string
	if sess != nil {
		loopback := loopbackPorts(cfg, p)
		for i, w := range web {
			local, err := e.forwardWeb(ctx, sess, run, w.Port, loopback)
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
		if err = e.publishSite(run, web, webLocals, docker); err == nil {
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

// waitConnected waits for the server's session, for at most connectWait. It
// fails when the session gives up (authentication failed) or ctx ends too.
// A start that is not interrupted waits on, and the project shows the
// session's error then (see waitingLocked).
func (e *Engine) waitConnected(ctx context.Context, sess transport.Transport) error {
	wctx, cancel := context.WithTimeout(ctx, e.connectWait)
	defer cancel()
	return sess.WaitConnected(wctx)
}

// ensureHosts asks for every hosts file name of the configuration (see
// config.HostsFileNames) to resolve, plus the exact hosts of web, the routes
// of the project being started, leaving out the names DNS already sends to
// this machine (see hosts.Wanted). The configuration's list holds only the
// route hosts of enabled projects, so a disabled project started by hand
// brings its own; without them its real-domain hosts would not resolve here.
// Calls are serialised so two starting projects never prompt for a password
// at the same time. Both the wait for the turn and the update itself end
// when ctx does.
func (e *Engine) ensureHosts(ctx context.Context, web []config.WebHosts) error {
	if e.opts.EnsureHosts == nil {
		return nil
	}
	select {
	case e.hostsTurn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-e.hostsTurn }()
	e.mu.Lock()
	names := append([]string(nil), e.hostNames...)
	e.mu.Unlock()
	for _, w := range web {
		for _, h := range w.Hosts {
			h = strings.ToLower(h)
			if h != "localhost" && !hostname.IsPattern(h) {
				names = append(names, h)
			}
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	wanted := hosts.Wanted(ctx, names, e.opts.Lookup)
	// Looked at last, once mu has been taken: Shutdown cancels the starts
	// one by one under mu, so the turn can pass from a start it cancelled
	// to one it has yet to cancel, and only once mu is free are both
	// cancelled. An interrupted start never begins an update.
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.opts.EnsureHosts(ctx, wanted)
}

// claimPort reserves a local port for a forward of run. It describes the
// holder of the port, as ownerTextLocked does, when another one has it.
func (e *Engine) claimPort(run *projectRun, port int) (owner string, ok bool) {
	return e.claimPortFor(run.name, port)
}

// claimPortFor reserves a local port for owner, a project name or the owner
// of a passthrough. It describes the holder of the port, as ownerTextLocked
// does, when another owner has it.
func (e *Engine) claimPortFor(owner string, port int) (holder string, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if held, taken := e.ports[port]; taken && held != owner {
		return e.ownerTextLocked(held), false
	}
	e.ports[port] = owner
	return "", true
}

// releasePort gives up a local port claimed for a forward, and stops the
// forward's [::1] relay if it has one.
func (e *Engine) releasePort(port int) {
	e.mu.Lock()
	e.releasePortLocked(port)
	e.mu.Unlock()
}

// releasePortLocked is releasePort for a caller that holds e.mu.
func (e *Engine) releasePortLocked(port int) {
	delete(e.ports, port)
	proxy.ForgetForward(forwardAddr(port))
	if l, ok := e.relays[port]; ok {
		_ = l.Close()
		delete(e.relays, port)
	}
}

// loopbackPorts are the ports of the loopback routes in cfg and of p, the
// project being started, should cfg not hold it as it is; p may be nil.
// Each is forwarded from the same port on this machine.
func loopbackPorts(cfg *config.Config, p *config.Project) map[int]bool {
	ports := map[int]bool{}
	add := func(routes []config.Route) {
		for _, r := range routes {
			if r.Role == config.RoleLoopback {
				ports[r.Port] = true
			}
		}
	}
	for _, other := range cfg.Projects {
		add(other.Routes)
	}
	if p != nil {
		add(p.Routes)
	}
	return ports
}

// forwardsFrom reports whether run already has a forward from local.
func (e *Engine) forwardsFrom(run *projectRun, local int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range run.forwards {
		if f.local == local {
			return true
		}
	}
	return false
}

// forwardWeb forwards a web route's remote port to a free local port chosen
// here and returns that port. The forward is recorded on run, so a rollback
// cancels it; the proxy learns the port when the site is published.
//
// A free port is any that nothing holds, or that the system thinks nothing
// holds, so two kinds are passed over. One is a port in loopback, the ports
// of the loopback routes: such a port is free until its route is forwarded,
// which would then find it taken. The other is a port run already forwards
// from, which the claim below would let through, as run holds it. A port
// passed over is no attempt; webPortSkips bounds them.
func (e *Engine) forwardWeb(ctx context.Context, sess transport.Transport, run *projectRun, remote int, loopback map[int]bool) (int, error) {
	var last error
	for attempt, skips := 0, 0; attempt < webPortAttempts && skips < webPortSkips && ctx.Err() == nil; {
		local, err := e.freePort()
		if err != nil {
			return 0, err
		}
		if loopback[local] || e.forwardsFrom(run, local) {
			last = fmt.Errorf("local port %d is kept for another route", local)
			skips++
			continue
		}
		attempt++
		if holder, ok := e.claimPort(run, local); !ok {
			last = fmt.Errorf("local port %d is held by %s", local, holder)
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
func (e *Engine) forwardLoopbacks(ctx context.Context, sess transport.Transport, run *projectRun, p *config.Project) []string {
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

func (e *Engine) forwardLoopback(ctx context.Context, sess transport.Transport, run *projectRun, port int) error {
	// The config cannot know the ports Nearhost was started with, so a
	// loopback route on one of them is refused here: on some systems the
	// forward would take the port from Nearhost on 127.0.0.1.
	if slices.Contains(e.ownPorts(), port) {
		return fmt.Errorf("loopback port %d is Nearhost's own port; give the route another port or role", port)
	}
	// Two projects may not share a local port: the session would treat the
	// second forward as the first, and stopping either would cancel it.
	if owner, ok := e.claimPort(run, port); !ok {
		return fmt.Errorf("loopback port %d is already forwarded for %s", port, owner)
	}
	if l, err := netbind.Listen("tcp", netbind.LoopbackProbe(port)); err != nil {
		e.releasePort(port)
		return fmt.Errorf("loopback port %d is already in use on this machine%s: %w", port, e.heldBy(ctx, port), err)
	} else if err := l.Close(); err != nil {
		e.releasePort(port)
		return fmt.Errorf("loopback port %d: %w", port, err)
	}
	// A machine without the IPv6 loopback has only 127.0.0.1 to serve.
	switch l, err := relayIPv6(port); {
	case err == nil:
		e.mu.Lock()
		e.relays[port] = l
		e.mu.Unlock()
	case oserr.NoLoopbackFamily(err):
	case oserr.AddrInUse(err):
		e.releasePort(port)
		return fmt.Errorf("loopback port %d is already in use on this machine%s: %w", port, e.heldBy(ctx, port), err)
	default:
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

// ownPorts lists the ports Nearhost listens on on this machine: HTTPS, HTTP
// and the page port as the engine was started with them, 0 for no page.
func (e *Engine) ownPorts() []int {
	https, http := e.opts.HTTPSPort, e.opts.HTTPPort
	if https == 0 {
		https = 443
	}
	if http == 0 {
		http = 80
	}
	return []int{https, http, e.opts.WebPort}
}

// publishSite adds run's site to the proxy: each of web, sent to the local
// port at the same index of webLocals, and served to the Docker networks as
// well when docker is set. Other sites keep serving. It refuses a host
// another published site serves, checked under applyMu so two starts cannot
// both take it. The first site served to Docker starts the refresh loop.
func (e *Engine) publishSite(run *projectRun, web []config.WebHosts, webLocals []int, docker bool) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	if err := e.hostClashLocked(run, web); err != nil {
		e.mu.Unlock()
		return err
	}
	run.web, run.webLocals, run.site, run.docker, run.shut = web, webLocals, true, docker, false
	// The forwards the session has applied may be dialled from here on;
	// those it has not (the session dropped while they were added) stay
	// shut until it applies them.
	e.syncGatesLocked(run)
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if err := e.applySites(sites); err != nil {
		// The proxy keeps its previous configuration, which lacks this site.
		e.mu.Lock()
		run.site = false
		e.syncGatesLocked(run)
		run.web, run.webLocals, run.docker = nil, nil, false
		e.mu.Unlock()
		// The proxy's errors already say they are the proxy's.
		return err
	}
	if docker {
		e.mu.Lock()
		e.startDockerLoopLocked()
		e.mu.Unlock()
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
// exact host under another project's pattern does not clash here: the proxy
// sends it to the more specific match. config.HostClash refuses such a pair
// when a change brings it in, but a config saved before then may hold one,
// and it keeps serving.
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
