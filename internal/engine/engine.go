package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/proxy"
)

// State is a project's lifecycle state.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateError    State = "error"
	// StateDegraded is a running project whose server's SSH connection is
	// down. It returns to running when the server reconnects.
	StateDegraded State = "degraded"
)

// Options configures an Engine.
type Options struct {
	RunDir      string   // SSH control sockets
	StorageRoot string   // Caddy storage
	SSHPath     string   // ssh binary; empty means "ssh"
	SSHEnv      []string // extra environment for ssh (tests)
	HTTPSPort   int      // 0 means 443
	HTTPPort    int      // 0 means 80
	// EnsureHosts makes every given alias resolve to 127.0.0.1. It gets the
	// valid aliases of all configured projects, sorted, each time a project
	// starts; calls are never concurrent. nil disables hosts management.
	EnsureHosts func(aliases []string) error
}

// ProjectStatus is a snapshot of one project.
type ProjectStatus struct {
	State   State
	Message string // why the project is in State; empty when there is nothing to say
	Alias   string
	// WebLocalPort is the local port the proxy sends the alias to; 0 when the
	// site is not published.
	WebLocalPort int
}

// Engine owns the proxy, one SSH session per server and the runtime state of
// every project. All methods are safe for concurrent use.
//
// # Locking
//
// Locks are taken in this order; a later one may be taken while holding an
// earlier one, never the reverse:
//
//	lifeMu -> projectRun.op -> serverConn.ensure, hostsMu, applyMu -> mu -> Bus.mu
//
// mu guards all state and is the only lock readers (Status, Sites,
// ServerStatus) take. It is never held across anything that can block: no ssh
// subprocess, proxy apply, hosts update or process lookup runs under it. While
// holding it the engine only publishes events (which never blocks) and reads a
// session's status and forward states (a short mutex inside transport).
//
// The other locks exist to serialise one kind of slow operation each, and are
// held across it on purpose: lifeMu covers Start and Shutdown, a project's op
// covers its start and stop, a server's ensure covers (re)starting its
// session, hostsMu covers a hosts update, and applyMu covers changing the set
// of published sites together with applying it to the proxy, so the proxy
// always ends up with the latest set. serverConn.ensure, hostsMu and applyMu
// are never held together.
//
// A session calls back into the engine through Logf, from its supervisor and
// from AddForward and CancelForward, in each case while it may hold its
// forward lock (fwdMu) but never its state lock. The callback (serverLog)
// takes mu, reads the session's status and forward states, and between two
// short holds of mu may look up the process holding a port; it holds no
// engine lock during that lookup and never calls AddForward or CancelForward.
// The engine in turn never calls a session method that runs ssh while holding
// mu. So the only orders that exist are fwdMu -> mu -> session state lock, and
// they cannot deadlock.
type Engine struct {
	cfg     *config.Config // private copy, never changed after New
	opts    Options
	bus     *Bus
	proxy   *proxy.Proxy
	order   []string               // project names in config order, without duplicates
	runs    map[string]*projectRun // fixed set of keys after New
	byAlias map[string]string      // alias -> project name
	aliases []string               // valid aliases of all projects, sorted

	connectWait time.Duration // how long a start waits for its server
	// holderOf names the process listening on a local TCP port, or returns ""
	// when it cannot tell. Tests replace it.
	holderOf func(port int) string

	lifeMu  sync.Mutex
	hostsMu sync.Mutex
	applyMu sync.Mutex

	mu      sync.Mutex
	started bool
	closed  bool
	servers map[string]*serverConn
	ports   map[int]string // local port held by a forward -> project
}

// New creates an engine for a snapshot of cfg: later changes to cfg are not
// seen. Call Start before starting projects.
func New(cfg *config.Config, opts Options) *Engine {
	e := &Engine{
		cfg:         cloneConfig(cfg),
		opts:        opts,
		bus:         NewBus(),
		proxy:       proxy.New(proxy.Options{StorageRoot: opts.StorageRoot, HTTPSPort: opts.HTTPSPort, HTTPPort: opts.HTTPPort}),
		runs:        map[string]*projectRun{},
		byAlias:     map[string]string{},
		connectWait: 30 * time.Second,
		holderOf:    portHolder,
		servers:     map[string]*serverConn{},
		ports:       map[int]string{},
	}
	for _, p := range e.cfg.Projects {
		if _, dup := e.runs[p.Name]; dup {
			continue // like config.Project, the first of a name wins
		}
		e.order = append(e.order, p.Name)
		e.runs[p.Name] = &projectRun{name: p.Name, alias: p.Alias, server: p.Server, state: StateStopped, announced: StateStopped}
		if _, taken := e.byAlias[p.Alias]; !taken {
			e.byAlias[p.Alias] = p.Name
		}
		if validAlias(p.Alias) == nil {
			e.aliases = append(e.aliases, p.Alias)
		}
	}
	sort.Strings(e.aliases)
	return e
}

// validAlias applies the hosts package's rules to one alias.
func validAlias(alias string) error {
	_, err := hosts.Rewrite("", []string{alias})
	return err
}

func cloneConfig(cfg *config.Config) *config.Config {
	out := &config.Config{}
	if cfg == nil {
		return out
	}
	out.Servers = append([]config.Server(nil), cfg.Servers...)
	out.Projects = append([]config.Project(nil), cfg.Projects...)
	for i := range out.Projects {
		out.Projects[i].Routes = append([]config.Route(nil), out.Projects[i].Routes...)
	}
	return out
}

// Events returns the event bus.
func (e *Engine) Events() *Bus { return e.bus }

// Config returns a copy of the configuration the engine was built with.
func (e *Engine) Config() *config.Config { return cloneConfig(e.cfg) }

// Start prepares the engine: it creates the run directory, checks that the
// proxy's HTTPS and HTTP ports can be bound (the HTTPS port on UDP as well), and loads the proxy with no
// sites, which creates the local CA and opens no listeners. If a port is
// taken it returns an error naming the port and the engine stays unstarted;
// Start can then be called again. Starting a started engine does nothing.
func (e *Engine) Start(ctx context.Context) error {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	e.mu.Lock()
	started, closed := e.started, e.closed
	e.mu.Unlock()
	if closed {
		return errShutDown
	}
	if started {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.opts.RunDir != "" {
		if err := os.MkdirAll(e.opts.RunDir, 0o700); err != nil {
			return fmt.Errorf("create run directory: %w", err)
		}
	}
	// With no sites the proxy opens no listeners, so a port conflict would
	// only show when the first project starts. Find it now.
	if err := e.probePort("HTTPS", e.opts.HTTPSPort, 443); err != nil {
		return err
	}
	if err := e.probePort("HTTP", e.opts.HTTPPort, 80); err != nil {
		return err
	}
	proxy.SetSink(proxy.LogFunc(e.proxyLog))
	e.applyMu.Lock()
	err := e.proxy.Apply(nil)
	e.applyMu.Unlock()
	if err != nil {
		proxy.SetSink(nil)
		return fmt.Errorf("start proxy: %w", err)
	}
	e.mu.Lock()
	e.started = true
	e.mu.Unlock()
	e.bus.Publish(Event{Message: "proxy ready"})
	return nil
}

// probePort reports whether the proxy could listen on port (def when 0): on
// TCP, and for HTTPS also on UDP, where the proxy serves HTTP/3.
func (e *Engine) probePort(what string, port, def int) error {
	if port == 0 {
		port = def
	}
	addr := fmt.Sprintf(":%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s port %d is in use%s: %w", what, port, e.heldBy(port), err)
	}
	if err := l.Close(); err != nil {
		return fmt.Errorf("%s port %d: %w", what, port, err)
	}
	if what != "HTTPS" {
		return nil
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("%s port %d (UDP, for HTTP/3) is not available: %w", what, port, err)
	}
	if err := pc.Close(); err != nil {
		return fmt.Errorf("%s port %d (UDP): %w", what, port, err)
	}
	return nil
}

// proxyLog receives the proxy's log lines; alias is empty for its own logs.
func (e *Engine) proxyLog(alias, line string) {
	project := alias
	if name, ok := e.byAlias[alias]; ok && alias != "" {
		project = name
	}
	e.bus.Publish(Event{Project: project, Message: line})
}

// Shutdown stops every project, ends every SSH master and stops the proxy. A
// start in progress is interrupted. It attempts every step even when one
// fails and returns all failures joined. The hosts file is left as it is.
// After Shutdown the engine cannot be started again; calling Shutdown again
// does nothing.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true // from here no project start gets past its first check
	started := e.started
	e.mu.Unlock()

	var errs []error
	for _, name := range e.order {
		if err := e.StopProject(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	// Every start has returned and none can begin; ensureSession creates and
	// starts nothing once closed is set.
	e.mu.Lock()
	conns := make([]*serverConn, 0, len(e.servers))
	for _, c := range e.servers {
		conns = append(conns, c)
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.stop()
		}()
	}
	wg.Wait()
	// Caddy and its log sink are process-global. If another engine has taken
	// the proxy over since Start, both are that engine's now: leave them.
	if started && e.proxy.Running() {
		e.applyMu.Lock()
		err := e.proxy.Stop()
		e.applyMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("stop proxy: %w", err))
		}
		proxy.SetSink(nil)
	}
	return errors.Join(errs...)
}

// Sites lists the sites published to the proxy, in config order.
func (e *Engine) Sites() []proxy.Site {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sitesLocked(nil)
}

// sitesLocked lists the published sites, leaving out the one of skip.
func (e *Engine) sitesLocked(skip *projectRun) []proxy.Site {
	var sites []proxy.Site
	for _, name := range e.order {
		run := e.runs[name]
		if run == skip || !run.site {
			continue
		}
		sites = append(sites, proxy.Site{Alias: run.alias, Upstream: fmt.Sprintf("127.0.0.1:%d", run.webLocal)})
	}
	return sites
}

// Status reports a project's state. A running project whose server is not
// connected is reported as degraded, with the server's last error. An unknown
// project is reported as stopped with no alias.
func (e *Engine) Status(name string) ProjectStatus {
	run, ok := e.runs[name]
	if !ok {
		return ProjectStatus{State: StateStopped}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	st, msg := e.effectiveLocked(run)
	return ProjectStatus{State: st, Message: msg, Alias: run.alias, WebLocalPort: run.webLocal}
}
