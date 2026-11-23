package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/page"
	"github.com/nearhost/nearhost/internal/proxy"
)

// coreWatchInterval is how often the core looks at the config file, as the
// interface does. Tests shorten it.
var coreWatchInterval = time.Second

// coreMode is what sets one way of running the core apart from another:
// `nearhost up` in a terminal, or the core in the background.
type coreMode struct {
	out       io.Writer // where events and summaries go
	startedBy control.StartedBy
	// keepRunning keeps the core going when its first round of starts has
	// nothing to start, or serves nothing: a core in the background waits
	// for the config to give it something to do.
	keepRunning bool
	// hosts is the EnsureHosts the engine gets. nil keeps up's: the one of
	// the backend, which may ask for sudo, asked once a run.
	hosts func(ctx context.Context, aliases []string) error
	// watch applies changes to the config file as they are saved.
	watch bool
	// stamped says out puts the date and time before each line itself, so
	// event lines leave out their own time.
	stamped bool
}

// runCore runs the engine and serves the projects named in args, or every
// enabled one, until it is interrupted, stopped through the control API, or,
// unless mode.keepRunning, finds nothing to serve. It answers the control
// API on the run directory's socket while it serves.
func runCore(cmd *cobra.Command, b upBackend, flags serveFlags, args []string, mode coreMode) error {
	if err := flags.validate(); err != nil {
		return err
	}
	// Before anything reads the CA's directory, so this run serves from
	// where it lands.
	migrateCA(cmd.ErrOrStderr())
	// The watcher looks at the file before it is read, so a change saved
	// from here on is applied once the watcher runs, after the engine
	// starts.
	var watcher *config.Watcher
	if mode.watch {
		path, err := configPath(cmd)
		if err != nil {
			return err
		}
		watcher = config.NewWatcher(path)
	}
	cfg, cfgPath, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	// Absolute, as the state reports it: a core restarted from it runs in
	// another directory.
	if abs, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = abs
	}
	// A config file named that does not exist is a mistake, but for the
	// core the interface starts: the interface offers to add the first
	// project, which makes the file.
	if msg := missingConfigMessage(cmd); msg != "" && len(cfg.Projects) == 0 && mode.startedBy != control.StartedTUI {
		return errors.New(msg)
	}
	names, err := upTargets(cfg, args)
	if err != nil {
		// A core that keeps running waits for the config to name something
		// to serve; a project named that does not exist is still refused.
		if !mode.keepRunning || len(args) > 0 {
			return err
		}
		names = nil
	}
	opts, err := engineOptions(b.ssh, flags, warnNoTerminal(cmd.ErrOrStderr(), b.ensureHosts), b.lookup)
	if err != nil {
		return err
	}
	if mode.hosts != nil && opts.EnsureHosts != nil {
		opts.EnsureHosts = mode.hosts
	}
	// Each start re-reads the project's .nearhostrc. A change that needs no
	// approval is saved to the config and served at once; one that does
	// waits for the interface.
	opts.Review = reviewProject(saveTo(cfgPath))
	// Agents can read the projects' status. What needs the user's answer is
	// reviewed while the page is served or a client follows the core's
	// stream, below: with no one to answer, the API refuses it.
	ag, agentNote := serveAgents(b.agents, &opts, cfg, cfgPath, flags.httpsPort, agentEnv{
		ssh: b.ssh, doctor: systemDoctorEnv(b.lookup, flags.noHosts, opts.StorageRoot), restart: true,
	})
	// Closed once, before the engine's shutdown lets go of the instance
	// lock: a core started as soon as that is free must find the agents'
	// socket free too. The deferred close covers a run that ends earlier.
	closeAgents := sync.OnceFunc(ag.close)
	defer closeAgents()
	eng := engine.New(cfg, opts)
	ag.attach(eng)
	// The printer and the approval lines' writer share the output from
	// goroutines of their own: each Write goes out whole, whatever the
	// output is.
	out := &lockedWriter{w: mode.out}
	p := &upPrinter{out: out, eng: eng, keepRunning: mode.keepRunning, stamped: mode.stamped}
	// The clients of the control API see what the engine publishes. The
	// engine publishes an event once its state has changed, so a client
	// that fetches the state on an event finds the change in it.
	hub := control.NewHub()
	// What the agents ask waits in the queue, which streams it to the
	// clients.
	queue := newApprovals(hub, time.Now)
	defer queue.close()
	ops := &coreOps{eng: eng, agents: ag, approvals: queue, path: cfgPath, httpsPort: flags.httpsPort}

	events, unsubscribe := eng.Events().Subscribe(eventBuffer)
	defer unsubscribe()
	stopForwarding := forwardEvents(eng.Events(), hub)
	defer stopForwarding()
	if agentNote != "" {
		eng.Events().Publish(engine.Event{Message: agentNote})
	}
	ag.publishHeld(eng)
	// A Stop through the control API ends base, and so ctx, as the first
	// signal does: the signal after it hurries the shutdown, as a second
	// signal does.
	base, stopCore := context.WithCancel(cmd.Context())
	defer stopCore()
	ctx, again, release := b.notify(base)
	defer release()
	if err := b.start(eng, ctx); err != nil {
		if engine.IsAlreadyRunning(err) {
			return alreadyRunning{err}
		}
		return b.bind.advise(err)
	}
	core := &coreBackend{
		coreOps: ops, hub: hub, ctx: ctx, stop: stopCore,
		httpPort: flags.httpPort, webPort: eng.WebPort(), // 0 once the page could not be served
		noHosts: flags.noHosts,
		info: control.Info{
			API: control.APIVersion, Version: Version, PID: os.Getpid(), Started: time.Now(),
			StartedBy: mode.startedBy, Reloads: mode.watch,
		},
	}
	// The core looks for a new release for as long as it runs, and says so
	// once, through the events every client already follows. Its own
	// context ends it, with the core's.
	var updated sync.WaitGroup
	defer updated.Wait()
	if updateChecksAllowed(os.Getenv, Version) {
		core.updates = newUpdateWatch(eng.Events().Publish)
		updated.Add(1)
		go func() {
			defer updated.Done()
			core.updates.run(ctx)
		}()
	}

	// Nearhost's page shows the core's status and takes answers while it
	// is served, from the core in this process, never through the control
	// socket. It is put in place before any link to it is given: an
	// approval's, or the control API's. It is cleared before the engine
	// stops, and its event streams end with it.
	closePage := func() {}
	if eng.WebPort() != 0 {
		pageDone := make(chan struct{})
		proxy.SetConsole(page.New(page.Options{
			Core: core, Watch: hub.Watch, Trees: ag.pageTrees(), Now: time.Now, Done: pageDone,
		}))
		closePage = sync.OnceFunc(func() {
			proxy.SetConsole(nil)
			close(pageDone)
		})
	}
	defer closePage()

	// Each request is said where the core's events are, with where to
	// answer it. The agent API tells the queue with its lock held, so the
	// line is handed to a writer of its own, never written there: output
	// that blocks must not hold up the agents, nor the answers. The page's
	// address is fixed for the run once the engine has started.
	pageURL, _ := eng.PageURL() // "" when the page is not served
	lines := newLineWriter(out, approvalLinesBuffer)
	defer lines.close()
	queue.onAdd = func(a control.Approval) {
		line := approvalLine(a, pageURL)
		if !mode.stamped {
			line = time.Now().Format("15:04:05") + " " + line
		}
		lines.put(line)
	}
	// The queue is the agents' reviewer whenever someone can answer: for
	// the whole run when the page is served, as its web port is fixed once
	// the engine has started; otherwise only while a client follows the
	// stream, so no new request waits on a reviewer that is not there. One
	// already queued when the last client goes waits until it expires.
	if eng.WebPort() != 0 {
		ag.review(queue)
	} else {
		hub.OnAttached(func(attached bool) {
			if attached {
				ag.review(queue)
			} else {
				ag.review(nil)
			}
		})
	}

	ctl, err := control.Listen(control.SocketPath(opts.RunDir), core, hub)
	if err != nil {
		// The engine's instance lock has already refused any other core
		// with this run directory, so this is one that got past it.
		if errors.Is(err, control.ErrInUse) {
			err = errors.New("Another Nearhost answers on " + control.SocketPath(opts.RunDir) + ". " + stopAdvice)
		} else {
			err = fmt.Errorf("serve the control socket: %w", err)
		}
		// The page and the agents' socket go first, as in the shutdown
		// below, so the lock is never free while they still answer.
		closePage()
		closeAgents()
		stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if stopErr := b.shutdown(eng, stopCtx); stopErr != nil {
			return errors.Join(err, fmt.Errorf("shut down: %w", stopErr))
		}
		return err
	}
	closeControl := sync.OnceFunc(func() { ctl.Close() })
	defer closeControl()

	// The watcher has a context of its own: a core that stops because it
	// has nothing to serve was not interrupted, and ctx goes on.
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	watched := make(chan struct{})
	if mode.watch {
		go func() {
			defer close(watched)
			watchConfig(watchCtx, ops, watcher)
		}()
	} else {
		close(watched)
	}

	// Projects start side by side: one whose server is slow to answer does
	// not hold the others up, and a failure stays with its project.
	startErrs := make([]error, len(names))
	var starts sync.WaitGroup
	for i, name := range names {
		starts.Add(1)
		go func() {
			defer starts.Done()
			startErrs[i] = eng.StartProject(ctx, name)
		}()
	}
	started := make(chan struct{})
	go func() {
		starts.Wait()
		close(started)
	}()

	// Whether the system trusts the local CA is asked on the side, so a slow
	// trust store holds nothing up.
	trustHint := make(chan string, 1)
	go func() { trustHint <- untrustedCA(b.trust, opts.StorageRoot) }()
	nothing := p.untilInterrupted(ctx, events, started, trustHint, names, startErrs)

	// No client may talk to an engine that is stopping, nor a new config
	// reach it: the socket goes first, so one that asks from here on finds
	// no core. Agents go next, for the same reason, and so that their
	// socket is free before the lock is.
	closeControl()
	closePage()
	closeAgents()
	lines.close() // the agents ask no more: what they asked is said first
	stopWatch()

	// A second signal must not end the process: the ssh masters are outside
	// its process group and would be left running. It cuts the shutdown
	// short instead; the engine then gives up on what it was waiting for and
	// still ends every master.
	p.advise = false // what goes wrong while stopping is not for the user to fix
	fmt.Fprintln(p.out, "shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- b.shutdown(eng, stopCtx) }()
	var stopErr error
	hurry := again
	for stopping := true; stopping; {
		select {
		case ev := <-events:
			p.event(ev)
		case <-hurry:
			hurry = nil
			fmt.Fprintln(p.out, "stopping now")
			cancel()
		case stopErr = <-stopped:
			stopping = false
		}
	}
	<-started // Shutdown interrupted any start still in flight
	core.close()
	<-watched // an Apply under way when ctx ended has failed on the shut engine
	unsubscribe()
	for ev := range events {
		p.event(ev)
	}
	stopForwarding()
	if stopErr != nil {
		return fmt.Errorf("shut down: %w", stopErr)
	}
	fmt.Fprintln(p.out, "stopped")
	if nothing {
		// The summary has said why; Main adds nothing.
		return exitError{code: 1, msg: "nothing can be served"}
	}
	return nil
}

// stopAdvice is how to stop the Nearhost that is in the way, and see it.
const stopAdvice = "Stop it with `nearhost stop`, or see it with `nearhost status`."

// alreadyRunning is the engine's refusal to start beside another Nearhost,
// told how to stop the other from the command line. The engine's own
// "Stop it first." is for the interface, which says it on its home screen.
type alreadyRunning struct{ err error }

// Error is the engine's message, with stopAdvice for what to do.
func (e alreadyRunning) Error() string {
	return strings.TrimSuffix(e.err.Error(), " Stop it first.") + " " + stopAdvice
}

// Unwrap is the engine's refusal, so it still matches
// engine.ErrAlreadyRunning.
func (e alreadyRunning) Unwrap() error { return e.err }

// watchConfig applies the config file, which w watches, to the engine
// through ops each time a change to it has settled, until ctx ends: one
// Apply at a time with the clients' reloads, and pruning the agents' grants
// as they do. A file that cannot be loaded or applied leaves the engine
// serving what it served, and that is said as an event of the engine's,
// which the printer prints like any other: the engine's own "config
// applied" says when one was applied.
func watchConfig(ctx context.Context, ops *coreOps, w *config.Watcher) {
	path := ops.path
	bad := func(err error) {
		if ctx.Err() != nil {
			return // cut short by the shutdown: the file is not at fault
		}
		msg := err.Error()
		// A file that does not parse is named by the error already.
		if !strings.Contains(msg, path) {
			msg = path + ": " + msg
		}
		ops.eng.Events().Publish(engine.Event{Message: msg + "; still serving the last good config"})
	}
	// What the watcher read and validated is applied: it refuses a file caught
	// empty or half saved. A client's Reload reads in Apply's turn, so it
	// never applies an older read over this one; should this one be older
	// than a Reload's, the change after it is seen on the next poll.
	w.Run(ctx, coreWatchInterval, func(cfg *config.Config) {
		if err := ops.Apply(ctx, cfg); err != nil {
			bad(err)
		}
	}, bad)
}

// coreBackend is the engine, as the control API sees it.
type coreBackend struct {
	// coreOps does the actions the interface asks for.
	*coreOps
	// hub streams what happens in the core to its clients.
	hub  *control.Hub
	info control.Info
	// ctx is the core's: it ends when the shutdown begins, and so do the
	// retries HostsChanged starts.
	ctx context.Context
	// stop begins the shutdown, as the first signal does.
	stop func()
	// httpPort and webPort are ports the core serves on, beside coreOps'
	// httpsPort.
	httpPort, webPort int
	// noHosts is set when the core was started with --no-hosts.
	noHosts bool
	// updates is the check for a new release; nil when this core makes none.
	updates *updateWatch
	// retries counts the starts HostsChanged has under way, which the
	// shutdown waits for. closed, under mu, is set once it has begun: a
	// request still being answered then starts nothing.
	retries sync.WaitGroup
	mu      sync.Mutex
	closed  bool
}

var _ control.Backend = (*coreBackend)(nil)

// Info says what this core is.
func (c *coreBackend) Info() control.Info { return c.info }

// Status lists the servers, this machine first as the interface does, and
// the projects that are enabled or are not stopped.
func (c *coreBackend) Status() control.Status {
	cfg := c.eng.Config()
	st := control.Status{Info: c.info, Servers: []control.ServerInfo{}, Projects: []control.ProjectInfo{}, Approvals: []control.ApprovalInfo{}}
	st.Page, _ = c.eng.PageURL() // "" when the page is not served
	st.Update = c.updates.latestNewer()
	for _, name := range serverNames(cfg) {
		status, msg := c.eng.ServerStatus(name)
		st.Servers = append(st.Servers, control.ServerInfo{Name: name, State: string(status), Message: msg})
	}
	for _, p := range cfg.Projects {
		ps := c.eng.Status(p.Name)
		if !p.Enabled && ps.State == engine.StateStopped {
			continue
		}
		st.Projects = append(st.Projects, control.ProjectInfo{
			Name: p.Name, State: string(ps.State), Message: ps.Message, URL: siteURL(p.Alias, c.httpsPort),
		})
	}
	for _, a := range c.approvals.Pending() {
		info := control.ApprovalInfo{ID: a.ID, Summary: approvalSummary(a)}
		if st.Page != "" {
			info.URL = st.Page + "/approve/" + a.ID
		}
		st.Approvals = append(st.Approvals, info)
	}
	for _, p := range cfg.Projects {
		change := c.eng.Status(p.Name).Pending
		if change == nil {
			continue
		}
		names := make([]string, len(change.Items))
		for i, it := range change.Items {
			names[i] = printable(it.Name())
		}
		info := control.ApprovalInfo{ID: p.Name, Summary: fmt.Sprintf("%s's %s asks for %s", printable(p.Name), nearhostrc.FileName, strings.Join(names, ", "))}
		if st.Page != "" {
			info.URL = st.Page + "/change/" + url.PathEscape(p.Name)
		}
		st.Approvals = append(st.Approvals, info)
	}
	return st
}

// State is what this core is and serves, and the ports it serves on.
func (c *coreBackend) State() control.State {
	st := c.coreOps.State()
	st.Info, st.HTTPPort, st.WebPort, st.NoHosts = c.info, c.httpPort, c.webPort, c.noHosts
	return st
}

// close waits for the starts HostsChanged began, and makes it start no more.
func (c *coreBackend) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.retries.Wait()
}

// serverNames lists this machine, then the configured servers.
func serverNames(cfg *config.Config) []string {
	names := []string{config.LocalServer}
	for _, s := range cfg.Servers {
		names = append(names, s.Name)
	}
	return names
}

// Stop begins the shutdown and returns at once.
func (c *coreBackend) Stop() { c.stop() }

// HostsChanged starts again, side by side, every enabled project that a start
// left in error, or stopped with a reason, which only a refused hosts file
// gives (see upPrinter.fix). It returns without waiting for them: the client
// does not wait long, and a start can take a while.
func (c *coreBackend) HostsChanged(context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ctx.Err() != nil {
		return
	}
	for _, p := range c.eng.Config().Projects {
		if !p.Enabled {
			continue
		}
		st := c.eng.Status(p.Name)
		if st.State != engine.StateError && (st.State != engine.StateStopped || st.Message == "") {
			continue
		}
		c.retries.Add(1)
		go func() {
			defer c.retries.Done()
			c.eng.StartProject(c.ctx, p.Name)
		}()
	}
}
