package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// State is a project's lifecycle state.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateError    State = "error"
	// StateDegraded is a project with a published site whose server's SSH
	// connection is down. It returns to the state it had (running, or the
	// error it started with) when the server reconnects.
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
	// WebPort is a port of the proxy's own that always serves Nearhost's
	// page at https://localhost:<port>. 0 means none. If the port is taken
	// when the engine starts, the proxy runs without the page.
	WebPort int
	// Capture keeps each request the proxy passes on, and its response, in
	// memory, for Captured and Replay.
	Capture bool
	// Version is the Nearhost version, shown on the proxy's page at
	// https://localhost. Empty means dev.
	Version string
	// EnsureHosts makes every given name resolve to 127.0.0.1. It gets the
	// hosts file names of the configuration (config.HostsFileNames: valid
	// aliases and the exact hosts of web routes), less those Lookup finds DNS
	// already sends here, sorted, each time a project starts; calls are
	// never concurrent. ctx ends when that start is interrupted (StopProject,
	// Shutdown, or the context given to StartProject); the function must
	// then give up and return, including when it is waiting for a password.
	// nil disables hosts management.
	EnsureHosts func(ctx context.Context, names []string) error
	// Lookup finds the addresses DNS gives a name, so EnsureHosts is only
	// asked for names that need a hosts entry. nil keeps every name.
	Lookup hosts.Lookup
	// Agent gives the socket each server's session forwards for coding
	// agents, the first time the session starts; false forwards none. It may
	// block, and is called once per session, never under the engine's lock.
	// nil forwards none.
	Agent func(server string) (transport.AgentForward, bool)
	// AgentDone is told a server's session has gone for good, as a reload
	// that removes the server does, so what Agent set up for it can go
	// too. It is called once per session Agent was asked about, never under
	// the engine's lock. nil does nothing.
	AgentDone func(server string)
	// Review re-reads a project's .nearhostrc through files, each time the
	// project starts and on Recheck, and says what it found (see Review).
	// What needs no approval it saves to the config file itself before it
	// returns. It may block, and is never called under the engine's lock;
	// ctx ends when the start is interrupted. nil reads no .nearhostrc.
	Review func(ctx context.Context, p config.Project, files discovery.FileReader) Review
	// DockerRanges gives the subnets of the local Docker daemon's networks,
	// which projects that opt in with docker_clients are served to besides
	// loopback. It is called with no engine lock held: when such a project
	// starts while Docker has never answered, and then every DockerRefresh
	// while one runs. nil means dockernet.Ranges.
	DockerRanges func(ctx context.Context) ([]string, error)
	// DockerRefresh is how often DockerRanges is asked again while a project
	// that opts in runs, so a network created later is served too. 0 means
	// 30s.
	DockerRefresh time.Duration
}

// ProjectStatus is a snapshot of one project.
type ProjectStatus struct {
	State   State
	Message string // why the project is in State; empty when there is nothing to say
	Alias   string
	// WebLocalPort is the local port the proxy sends the alias to, that of
	// the primary (first) web route; 0 when the site is not published.
	WebLocalPort int
	// WebLocalPorts maps the port of each web route to the local port the
	// proxy sends that route's hosts to; nil when the site is not published.
	WebLocalPorts map[int]int
	// Pending is the change the project's .nearhostrc asks for that waits
	// for approval; nil when there is none. The project serves what was
	// approved meanwhile. It is shared: do not change it.
	Pending *Change
	// Notice is why the project's .nearhostrc could not be taken into
	// account at its last read; empty when nothing went wrong.
	Notice string
}

// Engine owns the proxy, one SSH session per server and the runtime state of
// every project. All methods are safe for concurrent use.
//
// # Locking
//
// Locks are taken in this order; a later one may be taken while holding an
// earlier one, never the reverse:
//
//	lifeMu -> projectRun.op -> serverConn.ensure, hostsTurn, applyMu -> mu -> Bus.mu
//
// mu guards all state and is the only lock readers (Status, Sites,
// ServerStatus) take. It is never held across anything that can block: no ssh
// subprocess, proxy apply, hosts update or process lookup runs under it. While
// holding it the engine only publishes events (which never blocks) and reads a
// session's status and forward states (a short mutex inside transport).
//
// The other locks exist to serialise one kind of slow operation each, and are
// held across it on purpose: lifeMu covers Start, Shutdown and Apply, a project's op
// covers its start and stop, a server's ensure covers (re)starting its
// session, hostsTurn covers a hosts update, and applyMu covers changing the
// set of published sites together with applying it to the proxy, so the proxy
// always ends up with the latest set. serverConn.ensure, hostsTurn and
// applyMu are never held together. hostsTurn is a channel rather than a
// mutex because a hosts update can wait for a person: a start that is
// interrupted while it waits for its turn gives up instead of queueing.
//
// A session calls back into the engine through Logf, from its supervisor and
// from AddForward and CancelForward, in each case while it may hold its
// forward lock (fwdMu) but never its state lock. The callback (serverLog)
// takes mu, reads the session's status and forward states and publishes. It
// starts no process and never calls AddForward or CancelForward, so it never
// holds up the session. The engine in turn never calls a session method that
// runs ssh while holding mu. So the only orders that exist are fwdMu -> mu ->
// session state lock, and they cannot deadlock.
//
// Which process holds the port of a lost forward is asked by a goroutine of
// its own (lookupHolder). It holds no lock while it asks, and takes mu
// afterwards to check that the answer still applies before publishing it.
// Shutdown ends these lookups and waits for them.
//
// The Docker networks are asked for the same way, with no lock held: by a
// start, before it publishes, and by the refresh loop (refreshDocker). An
// answer that changes them is applied under applyMu then mu, like a site
// change, and dropped if the loop was stopped meanwhile. Shutdown waits for
// the loop.
type Engine struct {
	opts  Options
	bus   *Bus
	proxy *proxy.Proxy

	connectWait time.Duration // how long a start waits for its server
	// holderOf names the process listening on a local TCP port, or returns ""
	// when it cannot tell or ctx ends first. Tests replace it.
	holderOf func(ctx context.Context, port int) string
	// listen and listenPacket are net.Listen and net.ListenPacket, which
	// probePort binds the proxy's ports with. Tests replace them.
	listen       func(network, addr string) (net.Listener, error)
	listenPacket func(network, addr string) (net.PacketConn, error)
	// applySites gives the proxy the set of sites that includes a project
	// being started. It is the proxy's Apply; tests wrap it.
	applySites func(sites []proxy.Site) error
	// refreshSites gives the proxy the sites again when the Docker networks
	// change, unless another engine has taken the proxy over. Tests wrap it.
	refreshSites func(sites []proxy.Site) error
	// ctx ends at Shutdown; the lookups started for lost forwards run under
	// it and are counted in lookups.
	ctx     context.Context
	stop    context.CancelFunc
	lookups sync.WaitGroup
	// dockerLoops counts the Docker refresh loops, which run under ctx too.
	dockerLoops sync.WaitGroup
	// starts counts the project starts Apply has begun. Shutdown waits for
	// them; each returns at once when it finds the engine closed.
	starts sync.WaitGroup

	lifeMu    sync.Mutex
	hostsTurn chan struct{} // holds one token while a hosts update runs
	applyMu   sync.Mutex

	mu sync.Mutex
	// The configuration and what is derived from it. Apply replaces all of
	// them together, under mu; a *config.Config is never changed once it is
	// here, so a copy of the pointer taken under mu can be read without it.
	cfg     *config.Config
	order   []string               // project names in config order, without duplicates
	runs    map[string]*projectRun // one per name in order
	byAlias map[string]string      // alias -> project name
	// hostNames are the names the hosts file is asked to hold, before DNS
	// filtering: config.HostsFileNames of cfg.
	hostNames []string

	started bool
	closed  bool
	// webPort is the port Nearhost's page is served on, 0 when there is none:
	// the option was 0, or the port was taken at Start.
	webPort int
	lookup  uint64 // number of the last holder lookup started
	servers map[string]*serverConn
	// held names the servers disconnected on purpose, so nothing starts
	// their sessions behind the user's back. It is kept by name, not on the
	// session, so a reload that replaces the session keeps it.
	held  map[string]bool
	ports map[int]string // local port held by a forward -> project name or passthroughOwner
	// relays are the [::1] listeners of loopback forwards, by port; they
	// go when the port is released.
	relays map[int]net.Listener
	// docker is what is known of the local Docker networks, shared by every
	// project that opts in, as they share the machine's Docker.
	docker dockerState
	// passthroughs are the temporary passthroughs that are open, by id.
	passthroughs map[string]*passthrough
	// now is the clock; tests replace it.
	now func() time.Time
}

// New creates an engine for a snapshot of cfg: later changes to cfg are not
// seen until they are given to Apply. Call Start before starting projects.
func New(cfg *config.Config, opts Options) *Engine {
	e := &Engine{
		opts:         opts,
		webPort:      opts.WebPort,
		bus:          NewBus(),
		proxy:        proxy.New(proxy.Options{StorageRoot: opts.StorageRoot, HTTPSPort: opts.HTTPSPort, HTTPPort: opts.HTTPPort, WebPort: opts.WebPort, Capture: opts.Capture, Version: opts.Version, HomeWithNoSites: true}),
		connectWait:  30 * time.Second,
		holderOf:     PortHolder,
		listen:       net.Listen,
		listenPacket: net.ListenPacket,
		hostsTurn:    make(chan struct{}, 1),
		servers:      map[string]*serverConn{},
		held:         map[string]bool{},
		ports:        map[int]string{},
		relays:       map[int]net.Listener{},
		passthroughs: map[string]*passthrough{},
		now:          time.Now,
	}
	e.applySites = e.proxy.Apply
	e.refreshSites = func(sites []proxy.Site) error {
		_, err := e.proxy.ApplyIfRunning(sites)
		return err
	}
	e.ctx, e.stop = context.WithCancel(context.Background())
	e.setConfigLocked(cloneConfig(cfg), nil)
	return e
}

// setConfigLocked makes next the engine's configuration. A project keeps its
// run unless its name is in replace; a project new to the engine, or in
// replace, gets a fresh stopped one. A run that is left out is marked gone.
func (e *Engine) setConfigLocked(next *config.Config, replace map[string]bool) {
	runs := map[string]*projectRun{}
	byAlias := map[string]string{}
	var order []string
	for _, p := range next.Projects {
		if _, dup := runs[p.Name]; dup {
			continue // like config.Project, the first of a name wins
		}
		order = append(order, p.Name)
		run := e.runs[p.Name]
		if run == nil || replace[p.Name] {
			run = &projectRun{name: p.Name, alias: p.Alias, server: p.Server, state: StateStopped, announced: StateStopped}
		}
		runs[p.Name] = run
		if _, taken := byAlias[p.Alias]; !taken {
			byAlias[p.Alias] = p.Name
		}
	}
	for name, run := range e.runs {
		if runs[name] != run {
			run.gone = true
		}
	}
	e.cfg, e.order, e.runs, e.byAlias, e.hostNames = next, order, runs, byAlias, next.HostsFileNames()
}

func cloneConfig(cfg *config.Config) *config.Config {
	out := &config.Config{}
	if cfg == nil {
		return out
	}
	out.Servers = append([]config.Server(nil), cfg.Servers...)
	out.Projects = append([]config.Project(nil), cfg.Projects...)
	for i := range out.Projects {
		out.Projects[i].Routes = cloneRoutes(out.Projects[i].Routes)
		out.Projects[i].Nearhostrc = out.Projects[i].Nearhostrc.Clone()
	}
	return out
}

// cloneRoutes copies routes along with their hosts, so a copy of the
// configuration shares no slices with the original.
func cloneRoutes(routes []config.Route) []config.Route {
	if routes == nil {
		return nil
	}
	out := make([]config.Route, len(routes))
	for i, r := range routes {
		out[i] = r
		out[i].Hosts = append([]string(nil), r.Hosts...)
		if r.Hosts == nil {
			out[i].Hosts = nil
		}
	}
	return out
}

// Events returns the event bus.
func (e *Engine) Events() *Bus { return e.bus }

// WebPort returns the port Nearhost's page is served on, or 0 when there is
// none: the option was 0, or the port was taken when the engine started.
func (e *Engine) WebPort() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.webPort
}

// Config returns a copy of the engine's configuration.
func (e *Engine) Config() *config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneConfig(e.cfg)
}

// Start prepares the engine: it creates the run directory, checks that the
// proxy's HTTPS and HTTP ports, and any page port, can be bound (the HTTPS
// port on UDP as well), and loads the proxy with no sites, which creates the
// local CA and listens on every port to serve Nearhost's own page at
// localhost. If the HTTPS or HTTP port is taken it returns an error naming
// the port and the engine stays unstarted; Start can then be called again. A
// taken page port only publishes an event, and the proxy runs without the
// page. Starting a started engine does nothing.
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
	if err := differentProxyPorts(e.opts.HTTPSPort, e.opts.HTTPPort, e.opts.WebPort); err != nil {
		return err
	}
	if e.opts.RunDir != "" {
		if err := os.MkdirAll(e.opts.RunDir, 0o700); err != nil {
			return fmt.Errorf("create run directory: %w", err)
		}
	}
	// Name the program in the way, which Caddy's own error does not.
	if err := e.probePort(ctx, "HTTPS", e.opts.HTTPSPort, 443); err != nil {
		return err
	}
	if err := e.probePort(ctx, "HTTP", e.opts.HTTPPort, 80); err != nil {
		return err
	}
	// A taken page port costs only the page, not the proxy. A Start that
	// failed after dropping it tries the port again.
	e.proxy.SetWebPort(e.opts.WebPort)
	e.mu.Lock()
	e.webPort = e.opts.WebPort
	e.mu.Unlock()
	if port := e.opts.WebPort; port != 0 {
		if err := e.probePort(ctx, "Nearhost's page", port, 0); err != nil {
			e.proxy.SetWebPort(0)
			e.mu.Lock()
			e.webPort = 0
			e.mu.Unlock()
			reason := "is not available"
			var pe *portError
			if errors.As(err, &pe) {
				reason = pe.reason()
			}
			e.bus.Publish(Event{Message: fmt.Sprintf("port %d %s; serving without Nearhost's page (pass --web-port to choose another)", port, reason)})
		}
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

// differentProxyPorts refuses the same port for any two of HTTPS, HTTP and
// Nearhost's page (0 standing for 443 and 80, and for no page). Each
// can be bound by itself, so the probes would pass, but the proxy cannot
// serve two of them on one port and no site could be published.
func differentProxyPorts(httpsPort, httpPort, webPort int) error {
	if httpsPort == 0 {
		httpsPort = 443
	}
	if httpPort == 0 {
		httpPort = 80
	}
	if httpsPort == httpPort {
		return fmt.Errorf("the HTTPS and HTTP ports must differ (both are %d)", httpPort)
	}
	if webPort == httpsPort || webPort == httpPort {
		//lint:ignore ST1005 the message starts with the name Nearhost.
		return fmt.Errorf("Nearhost's page port must differ from the HTTPS and HTTP ports (it is %d)", webPort)
	}
	return nil
}

// portError is a port the proxy cannot listen on.
type portError struct {
	what string // "HTTPS", "HTTP" or "Nearhost's page"
	port int
	udp  bool // on UDP, for HTTP/3, rather than TCP
	// heldBy names the program holding a TCP port, as Engine.heldBy does:
	// " by node (pid 4242)", or "" when it is not known.
	heldBy string
	err    error
}

// reason says why the port is not available: "needs permission to bind" when
// the user may not bind it, or else that it is in use, by whom when known.
func (e *portError) reason() string {
	switch {
	case errors.Is(e.err, fs.ErrPermission):
		return "needs permission to bind"
	case e.udp:
		return "is not available"
	}
	return "is in use" + e.heldBy
}

// Error says which port is not available, and why.
func (e *portError) Error() string {
	if e.udp {
		return fmt.Sprintf("%s port %d (UDP, for HTTP/3) %s: %v", e.what, e.port, e.reason(), e.err)
	}
	return fmt.Sprintf("%s port %d %s: %v", e.what, e.port, e.reason(), e.err)
}

// Unwrap returns the error from binding the port.
func (e *portError) Unwrap() error { return e.err }

// probePort reports whether the proxy could listen on port (def when 0): on
// TCP, and for HTTPS also on UDP, where the proxy serves HTTP/3. A port it
// cannot bind is a *portError, which wraps EACCES or EPERM when the user may
// not bind the port. Any other failure on TCP is the port being in use, and
// names the program holding it when that can be found.
func (e *Engine) probePort(ctx context.Context, what string, port, def int) error {
	if port == 0 {
		port = def
	}
	addr := fmt.Sprintf(":%d", port)
	l, err := e.listen("tcp", addr)
	if err != nil {
		pe := &portError{what: what, port: port, err: err}
		if !errors.Is(err, fs.ErrPermission) {
			pe.heldBy = e.heldBy(ctx, port)
		}
		return pe
	}
	if err := l.Close(); err != nil {
		return fmt.Errorf("%s port %d: %w", what, port, err)
	}
	if what != "HTTPS" {
		return nil
	}
	pc, err := e.listenPacket("udp", addr)
	if err != nil {
		return &portError{what: what, port: port, udp: true, err: err}
	}
	if err := pc.Close(); err != nil {
		return fmt.Errorf("%s port %d (UDP): %w", what, port, err)
	}
	return nil
}

// proxyLog receives the proxy's log lines; alias is empty for its own logs.
func (e *Engine) proxyLog(alias, line string) {
	project := alias
	e.mu.Lock()
	if name, ok := e.byAlias[alias]; ok && alias != "" {
		project = name
	}
	e.mu.Unlock()
	e.bus.Publish(Event{Project: project, Message: line})
}

// Shutdown stops every project, ends every SSH master and stops the proxy. If
// another engine has taken the proxy over, the proxy is left to it untouched;
// this engine still cancels its forwards and ends its SSH masters. The masters
// run outside this process's group and are ended here whether or not ctx has
// ended; ctx only bounds stopping the projects. A start in
// progress is interrupted. It attempts every step even when one
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
	// Interrupt every start now rather than one by one as the projects are
	// stopped below, so that none goes on to a hosts update (and a password
	// prompt) in the meantime.
	for _, run := range e.runs {
		if run.cancel != nil {
			run.cancel()
		}
	}
	order := e.order
	e.mu.Unlock()
	e.stop() // no holder lookup begins once closed is set; end the running ones

	var errs []error
	for _, name := range order {
		if err := e.StopProject(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	e.starts.Wait() // starts Apply began: each finds the engine closed
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
	// A forward whose cancel failed kept its port, and its relay with it;
	// with the sessions gone, nothing is left for either to serve.
	e.mu.Lock()
	for port, l := range e.relays {
		_ = l.Close()
		delete(e.relays, port)
	}
	e.mu.Unlock()
	e.lookups.Wait()
	// The last site that opted in is gone, which ends the refresh loop, and
	// e.stop ended it in any case.
	e.dockerLoops.Wait()
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
		proxy.ClearCaptured()
	}
	return errors.Join(errs...)
}

// Sites lists the sites published to the proxy, in config order.
func (e *Engine) Sites() []proxy.Site {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sitesLocked(nil)
}

// sitesLocked lists the published sites, one per web route of each project,
// leaving out those of skip.
func (e *Engine) sitesLocked(skip *projectRun) []proxy.Site {
	var sites []proxy.Site
	for _, name := range e.order {
		run := e.runs[name]
		if run == skip || !run.site {
			continue
		}
		// A forward listens on 127.0.0.1 exactly. A local dev server may
		// listen on either loopback family (Vite's default host gives ::1
		// only), so the proxy dials it by name and tries both.
		host := "127.0.0.1"
		if run.server == config.LocalServer {
			host = "localhost"
		}
		var clients []string
		if run.docker {
			clients = e.dockerClientsLocked()
		}
		for i, w := range run.web {
			sites = append(sites, proxy.Site{Alias: run.alias, Hosts: w.Hosts, Upstream: fmt.Sprintf("%s:%d", host, run.webLocals[i]), Clients: slices.Clone(clients)})
		}
	}
	return sites
}

// Status reports a project's state. A running project whose server is not
// connected is reported as degraded, with the server's last error. An unknown
// project is reported as stopped with no alias. The state is the one
// announced last: the session of a server reports every change, and the
// engine announces what follows from it, so a state Status returns has
// always been published as an event first.
func (e *Engine) Status(name string) ProjectStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	run, ok := e.runs[name]
	if !ok {
		return ProjectStatus{State: StateStopped}
	}
	st, msg := run.announced, run.shownMsg
	if st == "" {
		// Recorded and not announced yet: only inside a start or stop, which
		// announces before it lets go of the lock.
		st, msg = e.effectiveLocked(run)
	}
	status := ProjectStatus{State: st, Message: msg, Alias: run.alias, Pending: run.pending, Notice: run.notice}
	if len(run.webLocals) > 0 {
		status.WebLocalPort = run.webLocals[0]
		status.WebLocalPorts = make(map[int]int, len(run.webLocals))
		for i, w := range run.web {
			status.WebLocalPorts[w.Port] = run.webLocals[i]
		}
	}
	return status
}

// Captured is the exchange the proxy kept under id, the nearhost_id of its
// access log line.
func (e *Engine) Captured(id string) (proxy.Exchange, bool) {
	return proxy.Captured(id)
}

// Replay sends the request the proxy kept under id again, through the
// proxy, and returns the response's status. The new exchange is logged and
// kept as usual.
func (e *Engine) Replay(ctx context.Context, id string) (int, error) {
	ex, ok := proxy.Captured(id)
	if !ok {
		return 0, errors.New("that request is no longer kept")
	}
	return proxy.Replay(ctx, ex, e.opts.StorageRoot, e.opts.HTTPSPort)
}
