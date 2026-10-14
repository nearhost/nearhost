package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/nearhost/nearhost/internal/config"
)

// applyPlan is what Apply does to move from one configuration to the next.
type applyPlan struct {
	stop    []string        // projects to stop, in the old config's order
	replace map[string]bool // projects whose run is replaced by a fresh one, or dropped
	start   []string        // projects to start once the new config is in place, in its order
	drop    []string        // servers whose session ends
}

// planApply works out an applyPlan. active reports whether a project of the
// old config is anything but stopped. Projects are matched by name.
//
//   - A project that is gone is stopped.
//   - A project whose server, path, alias or routes (ports and roles) changed,
//     or whose server's ssh_host changed or whose server is gone, is stopped
//     and given a fresh run; it is started again if it was active, or if it
//     has just been enabled, and is enabled.
//   - Otherwise a project that was enabled and is now disabled is stopped,
//     and one that was disabled and is now enabled is started.
//   - A new project is started when it is enabled.
//   - Everything else is left as it is, including a project stopped by hand.
func planApply(prev, next *config.Config, active func(name string) bool) applyPlan {
	plan := applyPlan{replace: map[string]bool{}}
	serverChanged := map[string]bool{}
	for _, s := range prev.Servers {
		if n, ok := next.Server(s.Name); !ok || n.SSHHost != s.SSHHost {
			serverChanged[s.Name] = true
			plan.drop = append(plan.drop, s.Name)
		}
	}
	seen := map[string]bool{}
	for _, p := range prev.Projects {
		if seen[p.Name] {
			continue // like config.Project, the first of a name wins
		}
		seen[p.Name] = true
		q, ok := next.Project(p.Name)
		switch {
		case !ok:
			plan.stop = append(plan.stop, p.Name)
			plan.replace[p.Name] = true
		case p.Server != q.Server || p.Path != q.Path || p.Alias != q.Alias || !sameRoutes(p.Routes, q.Routes) || serverChanged[p.Server]:
			plan.stop = append(plan.stop, p.Name)
			plan.replace[p.Name] = true
		case p.Enabled && !q.Enabled:
			plan.stop = append(plan.stop, p.Name)
		}
	}
	started := map[string]bool{}
	for _, q := range next.Projects {
		if started[q.Name] || !q.Enabled {
			continue
		}
		p, existed := prev.Project(q.Name)
		switch {
		case !existed,
			!p.Enabled,
			plan.replace[q.Name] && active(q.Name):
			started[q.Name] = true
			plan.start = append(plan.start, q.Name)
		}
	}
	return plan
}

// sameRoutes reports whether two route lists forward the same ports in the
// same roles. Labels are only shown, so a new label restarts nothing.
func sameRoutes(a, b []config.Route) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Port != b[i].Port || a[i].Role != b[i].Role {
			return false
		}
	}
	return true
}

// Apply moves the engine to cfg, as planApply describes, and returns once
// every stop has finished and every start has begun. A start's outcome shows
// in the project's state, not in the error. An invalid cfg is refused and
// nothing changes. If a project cannot be stopped, the projects stopped so
// far stay stopped, the old configuration stays in place, and the error is
// returned; applying again retries.
//
// Apply holds lifeMu while it stops projects and replaces the configuration,
// so it never overlaps Start, Shutdown or another Apply.
func (e *Engine) Apply(ctx context.Context, cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	next := cloneConfig(cfg)
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errShutDown
	}
	plan := planApply(e.cfg, next, func(name string) bool {
		run := e.runs[name]
		return run != nil && run.state != StateStopped
	})
	// A start that is waiting for a project's op while it is being replaced
	// must not go ahead with the old configuration.
	for name := range plan.replace {
		e.runs[name].gone = true
	}
	e.mu.Unlock()

	var errs []error
	for _, name := range plan.stop {
		if err := e.StopProject(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		e.mu.Lock()
		for name := range plan.replace {
			e.runs[name].gone = false
		}
		e.mu.Unlock()
		return fmt.Errorf("config not applied: %w", errors.Join(errs...))
	}
	for _, name := range plan.drop {
		e.dropServer(name)
	}

	e.mu.Lock()
	e.setConfigLocked(next, plan.replace)
	e.starts.Add(len(plan.start)) // under lifeMu, so before Shutdown can wait
	e.mu.Unlock()
	e.bus.Publish(Event{Message: "config applied"})
	for _, name := range plan.start {
		go func() {
			defer e.starts.Done()
			_ = e.StartProject(e.ctx, name) // the outcome is the project's state
		}()
	}
	return nil
}

// dropServer ends a server's session and forgets it, so the next start on a
// server of that name connects afresh. Its projects are stopped already.
func (e *Engine) dropServer(name string) {
	e.mu.Lock()
	c := e.servers[name]
	delete(e.servers, name)
	e.mu.Unlock()
	if c != nil {
		c.stop()
	}
}
