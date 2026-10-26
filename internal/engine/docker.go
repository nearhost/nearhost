package engine

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/dockernet"
)

// defaultDockerRefresh is how often the Docker networks are asked for again
// while a project that opts in runs, when Options.DockerRefresh is 0.
const defaultDockerRefresh = 30 * time.Second

// dockerState is what the engine knows of the local Docker networks. It is
// guarded by Engine.mu.
//
// Only the ranges Docker reports are served: none while Docker has never
// answered, since any guess at its networks (its default pools are in
// 172.16.0.0/12) also holds LAN addresses. Once it has answered, a later
// failure keeps the last ranges it gave.
type dockerState struct {
	ranges   []string // what Docker answered last; meaningless until answered
	answered bool     // Docker has answered at least once
	// stop ends the refresh loop; nil while none runs. The loop runs while
	// a published site is served to Docker.
	stop context.CancelFunc
	// loops counts refresh loops that have not returned. One that was
	// stopped may still be finishing while its successor runs.
	loops int
}

// dockerClientsLocked is the ranges a site that opts in is served to,
// besides loopback: none until Docker has answered.
func (e *Engine) dockerClientsLocked() []string {
	return e.docker.ranges
}

// askDocker asks Options.DockerRanges for the Docker networks. It is a
// network call: the caller holds no engine lock.
func (e *Engine) askDocker(ctx context.Context) ([]string, error) {
	ask := e.opts.DockerRanges
	if ask == nil {
		ask = dockernet.Ranges
	}
	return ask(ctx)
}

// describeRanges is ranges as shown in a project's log.
func describeRanges(ranges []string) string {
	if len(ranges) == 0 {
		return "none (Docker reports no subnets)"
	}
	return strings.Join(ranges, ", ")
}

// dockerStart settles the ranges run is about to be served to, asking
// Docker when it has never answered or when no refresh loop runs, so a
// start after a long idle uses the networks Docker has now rather than the
// last ones the loop saw, and logs them for the project. It
// holds no engine lock while it asks, as ensureHosts holds none while the
// hosts file is updated, and gives up when ctx ends. A Docker that does not
// answer does not fail the start: the project is served to loopback alone
// until it does.
func (e *Engine) dockerStart(ctx context.Context, run *projectRun) error {
	e.mu.Lock()
	answered := e.docker.answered
	ask := !answered || e.docker.stop == nil
	e.mu.Unlock()
	if ask {
		ranges, err := e.askDocker(ctx)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err == nil {
			e.useDockerRanges(ctx, ranges)
		} else {
			e.mu.Lock()
			answered = e.docker.answered // another start or the loop may have heard meanwhile
			e.mu.Unlock()
			if !answered {
				e.logProject(run, "Docker did not answer (%v); accepting requests from containers once it does", err)
				return nil
			}
		}
	}
	e.mu.Lock()
	ranges := slices.Clone(e.dockerClientsLocked())
	e.mu.Unlock()
	e.logProject(run, "accepts requests from Docker networks: %s", describeRanges(ranges))
	return nil
}

// useDockerRanges records an answer from Docker. When it changes what sites
// that opt in are served to, and some are published, it rebuilds the proxy
// configuration with them, as a site change does but without restarting a
// project, and logs the new list for each of them. If the proxy refuses the
// change, the engine keeps what the proxy still serves, so the next answer
// tries again. ctx is that of the caller: once it has ended (the start was
// interrupted, or the loop stopped) the answer is dropped.
//
// It takes applyMu and then mu, so the sites it builds are the latest set
// and a start publishing meanwhile builds them with the new ranges.
func (e *Engine) useDockerRanges(ctx context.Context, ranges []string) {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	if ctx.Err() != nil {
		e.mu.Unlock()
		return
	}
	prev := e.docker
	before := e.dockerClientsLocked()
	e.docker.ranges, e.docker.answered = ranges, true
	if slices.Equal(before, ranges) {
		e.mu.Unlock()
		return
	}
	var served []*projectRun
	for _, name := range e.order {
		if run := e.runs[name]; run.site && run.docker {
			served = append(served, run)
		}
	}
	if len(served) == 0 {
		e.mu.Unlock()
		return
	}
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if err := e.refreshSites(sites); err != nil {
		e.mu.Lock()
		e.docker.ranges, e.docker.answered = prev.ranges, prev.answered
		e.mu.Unlock()
		for _, run := range served {
			e.logProject(run, "could not accept requests from the new Docker networks (%s): %v", describeRanges(ranges), err)
		}
		return
	}
	for _, run := range served {
		e.logProject(run, "accepts requests from Docker networks: %s", describeRanges(ranges))
	}
}

// startDockerLoopLocked starts the refresh loop unless one runs or the
// engine is shut down. Shutdown sets closed under mu before it waits for
// the loops, so none is counted after it has begun waiting.
func (e *Engine) startDockerLoopLocked() {
	if e.docker.stop != nil || e.closed {
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	e.docker.stop = cancel
	e.docker.loops++
	e.dockerLoops.Add(1)
	go e.refreshDocker(ctx)
}

// dockerIdleLocked stops the refresh loop once no published site is served
// to Docker. The loop's ctx is ended under mu, so an answer it gets after
// that is dropped (see useDockerRanges).
func (e *Engine) dockerIdleLocked() {
	if e.docker.stop == nil {
		return
	}
	for _, run := range e.runs {
		if run.site && run.docker {
			return
		}
	}
	e.docker.stop()
	e.docker.stop = nil
}

// refreshDocker asks Docker for its networks every Options.DockerRefresh
// until ctx ends, holding no engine lock while it asks. An answer goes to
// useDockerRanges; a failure keeps what sites are served to, which is the
// last answer, or loopback alone while Docker has never answered.
func (e *Engine) refreshDocker(ctx context.Context) {
	defer e.dockerLoops.Done()
	defer func() {
		e.mu.Lock()
		e.docker.loops--
		e.mu.Unlock()
	}()
	every := e.opts.DockerRefresh
	if every <= 0 {
		every = defaultDockerRefresh
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if ctx.Err() != nil {
			return
		}
		if ranges, err := e.askDocker(ctx); err == nil {
			e.useDockerRanges(ctx, ranges)
		}
	}
}
