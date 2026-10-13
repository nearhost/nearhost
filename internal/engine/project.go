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
	webLocal  int                // local port the proxy sends the alias to; 0 when none
	site      bool               // the site is published to the proxy
	forwards  []forward          // forwards this project added to its server's session
	cancel    context.CancelFunc // interrupts a start in progress; nil otherwise
}

// StartProject brings a project up: it connects the project's server, makes
// its alias resolve, adds its forwards and publishes its site.
//
// A local project needs no forwards: the proxy sends the alias straight to
// the web route's port and loopback routes are already local. For a remote
// project the web route is forwarded to a free local port and each loopback
// route to the same port number on this machine.
//
// Starting a project that is running or already starting is a no-op that
// returns nil at once; it never adds a second set of forwards. The one
// exception: when the project's server gave up connecting (authentication
// failed), the connection is tried again, and the project leaves the degraded
// state once it is back. Starting a project in the error state first removes
// whatever its last attempt left.
//
// Outcomes other than running, each returned as an error as well:
//   - the server cannot be reached, the web forward fails, or the proxy
//     rejects the site: state error, nothing left running for the project;
//   - a loopback port is in use on this machine or held by another project:
//     state error naming the port, with the web route and the other loopback
//     routes still served;
//   - the hosts file could not be updated: state stopped with a message
//     saying the alias is not resolvable, nothing started for the project.
//
// A failure never changes another project.
func (e *Engine) StartProject(ctx context.Context, name string) error {
	p, ok := e.cfg.Project(name)
	if !ok {
		return fmt.Errorf("unknown project %q", name)
	}
	run := e.runs[name]
	e.mu.Lock()
	busy := run.state == StateStarting || run.state == StateRunning
	c := e.servers[run.server]
	e.mu.Unlock()
	if busy {
		// Nothing to add. The one thing worth doing for a running project is
		// reviving a server connection that gave up; the session re-applies
		// the project's forwards when it connects.
		if srv, ok := e.cfg.Server(p.Server); ok && c != nil && c.sess.Status() == transport.StatusAuthFailed {
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
	case run.state == StateRunning: // another start finished while this one waited
		e.mu.Unlock()
		return nil
	}
	run.cancel = cancel
	e.setStateLocked(run, StateStarting, "")
	e.mu.Unlock()

	st, err := e.bringUp(ctx, p, run)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	e.mu.Lock()
	run.cancel = nil
	e.setStateLocked(run, st, msg)
	e.mu.Unlock()
	return err
}

// bringUp does the work of StartProject and returns the state the project
// ends in. The caller holds run.op.
func (e *Engine) bringUp(ctx context.Context, p *config.Project, run *projectRun) (State, error) {
	web, ok := p.WebRoute()
	if !ok {
		return StateError, fmt.Errorf("project %q has no web route", p.Name)
	}
	srv, ok := e.cfg.Server(p.Server)
	if !ok {
		return StateError, fmt.Errorf("project %q uses unknown server %q", p.Name, p.Server)
	}
	if err := validAlias(p.Alias); err != nil {
		return StateError, fmt.Errorf("project %q: %w", p.Name, err)
	}
	// Leftovers of a start that ended in error go first, so their forwards
	// are cancelled rather than added to.
	if err := e.tearDown(ctx, run); err != nil {
		return StateError, fmt.Errorf("remove previous start: %w", err)
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
	if err := e.ensureHosts(); err != nil {
		return StateStopped, fmt.Errorf("alias %s is not resolvable: the hosts file was not updated (%w); start the project again to retry", p.Alias, err)
	}

	webLocal := web.Port
	var problems []string
	if sess != nil {
		var err error
		if webLocal, err = e.forwardWeb(ctx, sess, run, web.Port); err != nil {
			return StateError, fmt.Errorf("web port %d: %w", web.Port, err)
		}
		problems = e.forwardLoopbacks(ctx, sess, run, p)
	}
	err := ctx.Err()
	if err == nil {
		if err = e.publishSite(run, webLocal); err != nil {
			err = fmt.Errorf("proxy: %w", err)
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

// ensureHosts asks for every valid alias to resolve. Calls are serialised so
// two starting projects never prompt for a password at the same time.
func (e *Engine) ensureHosts() error {
	if e.opts.EnsureHosts == nil {
		return nil
	}
	e.hostsMu.Lock()
	defer e.hostsMu.Unlock()
	return e.opts.EnsureHosts(append([]string(nil), e.aliases...))
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

// forwardWeb forwards the web route to a free local port chosen here and
// returns that port.
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
		run.webLocal = local
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
		return fmt.Errorf("loopback port %d is already in use on this machine: %w", port, err)
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

// publishSite adds run's site to the proxy. Other sites keep serving.
func (e *Engine) publishSite(run *projectRun, webLocal int) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	run.webLocal, run.site = webLocal, true
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if err := e.proxy.Apply(sites); err != nil {
		// The proxy keeps its previous configuration, which lacks this site.
		e.mu.Lock()
		run.site = false
		e.mu.Unlock()
		return err
	}
	return nil
}

// rollBack undoes a start that failed part way. It runs even when the start's
// own context has ended, because forwards must still be cancelled.
func (e *Engine) rollBack(ctx context.Context, run *projectRun) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := e.tearDown(ctx, run); err != nil {
		return fmt.Errorf("undo start: %w", err)
	}
	return nil
}

// tearDown removes run's site from the proxy and cancels its forwards. It
// attempts every step and returns the failures joined. What could not be
// removed stays recorded on run, so calling it again retries exactly that.
// The caller holds run.op.
func (e *Engine) tearDown(ctx context.Context, run *projectRun) error {
	var errs []error

	e.applyMu.Lock()
	e.mu.Lock()
	published := run.site
	sites := e.sitesLocked(run)
	e.mu.Unlock()
	if published {
		if err := e.proxy.Apply(sites); err != nil {
			errs = append(errs, fmt.Errorf("remove site: %w", err))
		} else {
			e.mu.Lock()
			run.site = false
			e.mu.Unlock()
		}
	}
	e.applyMu.Unlock()

	e.mu.Lock()
	forwards := append([]forward(nil), run.forwards...)
	c := e.servers[run.server]
	e.mu.Unlock()
	var kept []forward
	for _, f := range forwards {
		if err := c.sess.CancelForward(ctx, f.local, f.remote); err != nil {
			kept = append(kept, f)
			errs = append(errs, err)
			e.logProject(run, "cancel forward: %v", err)
			continue
		}
		e.releasePort(f.local)
	}
	e.mu.Lock()
	run.forwards = kept
	if !run.site {
		run.webLocal = 0
	}
	e.mu.Unlock()
	return errors.Join(errs...)
}

// StopProject removes a project's site from the proxy and cancels its
// forwards; other projects keep serving and the server's SSH connection stays
// up. A start in progress is interrupted first. Stopping a stopped project
// does nothing.
//
// If a step fails the project ends in the error state and the error is
// returned; what could not be removed is remembered, and stopping (or
// starting) the project again retries it.
func (e *Engine) StopProject(ctx context.Context, name string) error {
	run, ok := e.runs[name]
	if !ok {
		return fmt.Errorf("unknown project %q", name)
	}
	e.mu.Lock()
	if run.cancel != nil {
		run.cancel()
	}
	e.mu.Unlock()
	run.op.Lock()
	defer run.op.Unlock()

	err := e.tearDown(ctx, run)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		err = fmt.Errorf("stop %s: %w", name, err)
		e.setStateLocked(run, StateError, err.Error())
		return err
	}
	e.setStateLocked(run, StateStopped, "")
	return nil
}
