package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
)

const (
	// eventBuffer is how many engine events may wait to be printed; the bus
	// drops what does not fit rather than hold the engine up.
	eventBuffer = 1024
	// shutdownTimeout bounds stopping every project and ssh master.
	shutdownTimeout = 30 * time.Second
)

// upBackend is the seam between `up` and the machine.
type upBackend struct {
	ssh sshClient
	// trust reads the system trust store, to say when the local CA is not
	// in it. up never installs anything.
	trust trustBackend
	// ensureHosts makes the aliases resolve to this machine and gives up when
	// ctx ends. It is not used with --no-hosts.
	ensureHosts func(ctx context.Context, aliases []string) error
	// lookup finds the addresses DNS gives a name, so the engine leaves out
	// of the hosts file the names DNS already sends to this machine.
	lookup hosts.Lookup
	// notify listens, from the call until the release, for the signals that
	// ask the process to stop. The context ends at the first of them (or
	// with parent) and starts the shutdown; the channel is closed at the
	// next one, which cuts the shutdown short. It is one subscription, so
	// no signal falls between the two.
	notify func(parent context.Context) (ctx context.Context, again <-chan struct{}, release func())
	// agents reaches what the agent API needs; a zero one serves no agents.
	agents agentsBackend
	// shutdown stops the engine once up has been interrupted, giving up
	// when ctx ends: eng.Shutdown, which tests hold open for a second
	// interrupt.
	shutdown func(eng *engine.Engine, ctx context.Context) error
	// start starts the engine: eng.Start, which tests replace to fail it.
	start func(eng *engine.Engine, ctx context.Context) error
	// bind is what up needs to say how to let the binary bind ports 80 and
	// 443; the zero value says nothing.
	bind bindEnv
}

// bindEnv is the machine's side of the advice printed when a proxy port
// needs a privilege.
type bindEnv struct {
	goos string // advice is for "linux" only
	// notPermitted reports whether an error from Start is a port the user
	// may not bind: engine.BindNotPermitted.
	notPermitted func(error) bool
	elevated     func() bool
	// exe is the running binary's path with symlinks resolved, or "".
	exe    func() string
	isFile func(path string) bool
	// unprivilegedPortStart is as in updateBackend.
	unprivilegedPortStart func() (int, bool)
}

// systemUp is the production backend. Only NewRoot wires it in, with the
// lookup it shares with the interface and doctor.
func systemUp(lookup hosts.Lookup) upBackend {
	return upBackend{
		trust:  systemTrust(),
		lookup: lookup,
		ensureHosts: func(ctx context.Context, aliases []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			// The applier ends sudo when ctx does, so an interrupt during
			// the password prompt shuts down instead of waiting for it.
			// sudo prompts only when stdin is a terminal (see
			// upHostsApplier).
			terminal := term.IsTerminal(int(os.Stdin.Fd()))
			_, err = hosts.Ensure(hosts.DefaultPath, aliases, upHostsApplier(ctx, elevate.System(), exe, terminal))
			return err
		},
		notify:   notifyStop,
		start:    (*engine.Engine).Start,
		shutdown: (*engine.Engine).Shutdown,
		agents:   systemAgents(),
		bind: bindEnv{
			goos: runtime.GOOS, notPermitted: engine.BindNotPermitted, elevated: isElevated, exe: realExecutable,
			isFile: isRegularFile, unprivilegedPortStart: readUnprivilegedPortStart,
		},
	}
}

// notifyStop is the notify of the production backend.
func notifyStop(parent context.Context) (context.Context, <-chan struct{}, func()) {
	// When the output is a pipe whose reader has gone (`up | tee`, then
	// Ctrl-C), the next line written would end the process on the spot and
	// leave the ssh masters running. Ignored, the write fails instead and
	// the shutdown runs to its end. It stays ignored after the release: the
	// last lines are written then.
	signal.Ignore(syscall.SIGPIPE)
	// While this is registered no stop signal ends the process, however many
	// arrive: those beyond the second are dropped.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, stopSignals()...)
	ctx, cancel := context.WithCancel(parent)
	again := make(chan struct{})
	released := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			if ctx.Err() != nil { // the shutdown had begun; this one hurries it
				close(again)
				return
			}
			cancel()
		case <-ctx.Done():
		case <-released:
			return
		}
		select {
		case <-sigs:
			close(again)
		case <-released:
		}
	}()
	var once sync.Once
	return ctx, again, func() {
		once.Do(func() {
			signal.Stop(sigs)
			close(released)
			cancel()
		})
	}
}

// stopSignals are the signals up and the interface shut down on. The ssh
// masters run outside this process's group, so nothing ends them when the
// terminal goes away: a hangup is therefore handled like Ctrl-C, unless it
// is being ignored (nohup), in which case it stays ignored.
func stopSignals() []os.Signal {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return sigs
}

func newUpCmd(b upBackend) *cobra.Command {
	var flags serveFlags
	cmd := &cobra.Command{
		Use:   "up [project...]",
		Short: "Start projects and serve them until interrupted",
		Long: "Start the named projects, or every enabled project when none is named,\n" +
			"and serve them at https://<alias> until interrupted (Ctrl-C).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUp(cmd, b, flags, args)
		},
	}
	flags.bind(cmd)
	flags.bindCapture(cmd)
	return cmd
}

func runUp(cmd *cobra.Command, b upBackend, flags serveFlags, args []string) error {
	if err := flags.validate(); err != nil {
		return err
	}
	// Before anything reads the CA's directory, so this run serves from
	// where it lands.
	migrateCA(cmd.ErrOrStderr())
	cfg, cfgPath, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	if msg := missingConfigMessage(cmd); msg != "" && len(cfg.Projects) == 0 {
		return errors.New(msg)
	}
	names, err := upTargets(cfg, args)
	if err != nil {
		return err
	}
	opts, err := engineOptions(b.ssh, flags, warnNoTerminal(cmd.ErrOrStderr(), b.ensureHosts), b.lookup)
	if err != nil {
		return err
	}
	// Each start re-reads the project's .nearhostrc. up never reloads the
	// config, so a change that needs no approval is saved for the next run
	// and served at once; one that does waits for the interface.
	opts.Review = reviewProject(saveTo(cfgPath))
	// Agents can read the projects' status. Without the interface there is
	// no one to review a proposal, so the API refuses them.
	ag, agentNote := serveAgents(b.agents, &opts, cfg, cfgPath, flags.httpsPort, agentEnv{
		ssh: b.ssh, doctor: systemDoctorEnv(b.lookup, flags.noHosts, opts.StorageRoot), restart: true,
	})
	defer ag.close()
	eng := engine.New(cfg, opts)
	ag.attach(eng)
	p := &upPrinter{out: cmd.OutOrStdout(), eng: eng, cfg: cfg}

	events, unsubscribe := eng.Events().Subscribe(eventBuffer)
	defer unsubscribe()
	if agentNote != "" {
		eng.Events().Publish(engine.Event{Message: agentNote})
	}
	ag.publishHeld(eng)
	ctx, again, release := b.notify(cmd.Context())
	defer release()
	if err := b.start(eng, ctx); err != nil {
		return b.bind.advise(err)
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
	unsubscribe()
	for ev := range events {
		p.event(ev)
	}
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

// untilInterrupted prints the engine's events until ctx ends and, once every
// start has returned (started is closed), the summary of names, whose starts
// returned startErrs, unless ctx has ended by then. The trust store's hint is
// printed after the summary, whichever comes first. Everything is printed
// from this goroutine, so lines never interleave.
//
// When the summary finds nothing served and nothing waiting (see
// nothingToServe), it returns at once and reports true: up then stops as it
// does when interrupted. That is checked once, after the first round of
// starts; a project that fails later leaves up running.
func (p *upPrinter) untilInterrupted(ctx context.Context, events <-chan engine.Event, started <-chan struct{}, trustHint <-chan string, names []string, startErrs []error) (nothing bool) {
	hint, summarised := "", false
	for {
		select {
		case ev := <-events:
			p.event(ev)
		case hint = <-trustHint:
			trustHint = nil
		case <-started:
			// An interrupt ends the starts too, so both can be ready at
			// once, and select picks either. Starts that returned because
			// of the interrupt are not summed up, as errors to fix.
			if ctx.Err() != nil {
				return false
			}
			started = nil
			p.buffered(events) // the state changes the summary sums up
			if p.summary(names, startErrs) {
				return true
			}
			p.advise = true // from here a state change is news, and so is its fix
			summarised = true
		case <-ctx.Done():
			return false
		}
		if summarised && hint != "" {
			fmt.Fprintln(p.out, hint)
			hint = ""
		}
	}
}

// untrustedCA returns the line to print when the system trust store does not
// trust the local CA, and "" when it does. Only the system store is asked:
// the NSS stores of browsers need an external tool and are left to
// `nearhost trust`. When the root cannot be read there is nothing to say
// either: the proxy has its own way of reporting a missing CA.
func untrustedCA(b trustBackend, storageRoot string) string {
	if b.rootCert == nil || b.isTrusted == nil {
		return ""
	}
	cert, err := b.rootCert(storageRoot)
	if err != nil || b.isTrusted(cert) {
		return ""
	}
	return "The Nearhost Local CA is not trusted on this machine, so browsers will show a certificate warning. Run `nearhost trust` to fix that."
}

// disabledClash refuses the first disabled project among names that would
// serve a host another enabled or named project serves.
func disabledClash(cfg *config.Config, names []string) error {
	started := *cfg
	started.Projects = append([]config.Project(nil), cfg.Projects...)
	for _, name := range names {
		q, _ := started.Project(name)
		q.Enabled = true
	}
	for _, name := range names {
		if p, _ := cfg.Project(name); p.Enabled {
			continue
		}
		q, _ := started.Project(name)
		if hc, clash := started.HostConflict(*q); clash {
			return fmt.Errorf("cannot start disabled project %q: %w", name, hc)
		}
	}
	return nil
}

// upTargets is the projects to start: those named, or every enabled one. A
// disabled project named is checked as if it were enabled, beside the other
// projects named: it is refused when it would serve a host another of them
// serves, as enabling it would be. A pair of enabled projects is left to
// serve, as it did before such pairs were refused.
func upTargets(cfg *config.Config, args []string) ([]string, error) {
	if len(cfg.Projects) == 0 {
		return nil, errors.New("no projects to start; add one with `nearhost add <path>`")
	}
	var names []string
	seen := map[string]bool{}
	for _, name := range args {
		if _, ok := cfg.Project(name); !ok {
			return nil, fmt.Errorf("unknown project %q; `nearhost ls` lists the configured projects", name)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(args) > 0 {
		return names, disabledClash(cfg, names)
	}
	for _, p := range cfg.Projects {
		if p.Enabled {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no enabled projects to start; name one, like `%s`", config.UpCommand(cfg.Projects[0].Name))
	}
	return names, nil
}

// engineOptions builds the engine's options from the standard directories.
// ensureHosts updates the hosts block and lookup filters the names it is
// given; neither is used with --no-hosts.
func engineOptions(ssh sshClient, f serveFlags, ensureHosts func(context.Context, []string) error, lookup hosts.Lookup) (engine.Options, error) {
	runDir, err := config.RunDir()
	if err != nil {
		return engine.Options{}, err
	}
	storage, err := config.CaddyDir()
	if err != nil {
		return engine.Options{}, err
	}
	opts := engine.Options{
		RunDir: runDir, StorageRoot: storage,
		SSHPath: ssh.path, SSHEnv: ssh.env,
		HTTPSPort: f.httpsPort, HTTPPort: f.httpPort, WebPort: f.webPort,
		Capture: !f.noCapture,
		Version: Version,
	}
	if !f.noHosts {
		opts.EnsureHosts = askOnce(ensureHosts)
		// Both `nearhost up` and the interface build their options here, so
		// both skip names whose public DNS already points at this machine.
		opts.Lookup = lookup
	}
	return opts, nil
}

// errNoTerminal is the error of a hosts update that needs sudo's password
// when there is no terminal to ask for it in. Only the Unix applier returns
// it (see upHostsApplier).
var errNoTerminal = errors.New("sudo needs a password, and there is no terminal to ask for it")

// hostsNoTerminal is what up prints when a hosts update fails with
// errNoTerminal.
const hostsNoTerminal = "The hosts file needs sudo, and there is no terminal to ask in. Run nearhost up in a terminal once, or pass --no-hosts."

// warnNoTerminal wraps a hosts function so that the first update that fails
// with errNoTerminal prints hostsNoTerminal to w. Later ones print nothing:
// the message is said once a run, however many projects or updates there
// are. A nil ensure stays nil.
func warnNoTerminal(w io.Writer, ensure func(context.Context, []string) error) func(context.Context, []string) error {
	if ensure == nil {
		return nil
	}
	var once sync.Once
	return func(ctx context.Context, aliases []string) error {
		err := ensure(ctx, aliases)
		if errors.Is(err, errNoTerminal) {
			once.Do(func() { fmt.Fprintln(w, hostsNoTerminal) })
		}
		return err
	}
}

// askOnce wraps a hosts function so that a refusal stands for the rest of the
// run. The engine asks again for every project it starts; without this, a
// declined password prompt would come back once per project. An update that
// was interrupted (its context ended) is not a refusal and is not remembered.
// The engine never makes two calls at the same time.
func askOnce(ensure func(context.Context, []string) error) func(context.Context, []string) error {
	var refused error
	return func(ctx context.Context, aliases []string) error {
		if refused != nil {
			return refused
		}
		err := ensure(ctx, aliases)
		if err != nil && ctx.Err() == nil {
			refused = err
		}
		return err
	}
}

// bindAdvice is an error from a proxy port the user may not bind, with the
// command that lets nearhost bind it.
type bindAdvice struct {
	err    error
	advice string
}

// Error is the original error, a blank line, then the advice.
func (e *bindAdvice) Error() string { return e.err.Error() + "\n\n" + e.advice }

// Unwrap returns the error from the engine.
func (e *bindAdvice) Unwrap() error { return e.err }

// advise adds to err the command that lets the binary bind the proxy's ports,
// when err is a port the user may not bind on Linux. Elsewhere, for root, on
// a system that lets any program bind from port 80, and for every other
// error, it returns err as it is.
func (e bindEnv) advise(err error) error {
	if e.goos != "linux" || e.notPermitted == nil || !e.notPermitted(err) {
		return err
	}
	if e.elevated != nil && e.elevated() {
		return err // root is refused for another reason
	}
	if e.unprivilegedPortStart != nil {
		if first, ok := e.unprivilegedPortStart(); ok && first <= 80 {
			return err // the capability is not what is missing
		}
	}
	exe := ""
	if e.exe != nil {
		exe = e.exe()
	}
	var advice string
	if exe != "" && e.isFile != nil && e.isFile(exe) {
		advice = doctor.SetcapFix(exe)
	} else {
		// Same words as the update's advice: a path that is no regular
		// file may be a launcher script, which a capability does nothing for.
		advice = "Nearhost could not tell which file is the binary, so check that it is the binary and not a launcher script before running:\n\n" + doctor.SetcapFix("")
	}
	return &bindAdvice{err: err, advice: advice + "\nDo not run nearhost with sudo."}
}
