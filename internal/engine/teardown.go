package engine

import (
	"context"
	"errors"
	"fmt"
)

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

// tearDown removes run's site from the proxy, unless the proxy has passed to
// another engine, and cancels its forwards. It attempts every step and
// returns the failures joined. What could not be removed stays recorded on
// run, so calling it again retries exactly that.
// The caller holds run.op and must record the resulting state afterwards
// (changeLocked or setStateLocked), which is what ends the teardown for
// Status and for announcements.
func (e *Engine) tearDown(ctx context.Context, run *projectRun) error {
	var errs []error

	// From here until the caller records the state the teardown leads to, the
	// project shows what it showed last. Its site and forwards go one by one,
	// and each forward cancel makes the session call back into the engine;
	// none of those half-way points is a state of the project.
	e.mu.Lock()
	run.tearing = true
	e.mu.Unlock()

	e.applyMu.Lock()
	e.mu.Lock()
	published := run.site
	sites := e.sitesLocked(run)
	e.mu.Unlock()
	if published {
		// Caddy is process-global. If another engine has taken the proxy
		// over, this engine's sites are already gone from it, and applying
		// here would take it back from that engine: then there is nothing to
		// remove, and ApplyIfRunning does nothing.
		if _, err := e.proxy.ApplyIfRunning(sites); err != nil {
			errs = append(errs, fmt.Errorf("remove site: %w", err))
		} else {
			e.mu.Lock()
			run.site = false
			e.mu.Unlock()
		}
	}
	e.applyMu.Unlock()

	// The forwards leave the record before they are cancelled, so a forward
	// the engine is removing is not taken for one the project lost. What was
	// known about lost ones goes with them; a lookup still running finds
	// nothing to add its answer to.
	e.mu.Lock()
	forwards := run.forwards
	run.forwards = nil
	run.holders = nil
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
		run.web, run.webLocals = nil, nil
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
	e.mu.Lock()
	run, ok := e.runs[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unknown project %q", name)
	}
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
