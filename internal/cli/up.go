package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
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
	// notify listens, from the call until the release, for the signals that
	// ask the process to stop. The context ends at the first of them (or
	// with parent) and starts the shutdown; the channel is closed at the
	// next one, which cuts the shutdown short. It is one subscription, so
	// no signal falls between the two.
	notify func(parent context.Context) (ctx context.Context, again <-chan struct{}, release func())
}

// systemUp is the production backend. Only NewRoot wires it in.
func systemUp() upBackend {
	return upBackend{
		trust: systemTrust(),
		ensureHosts: func(ctx context.Context, aliases []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			// The applier ends sudo when ctx does, so an interrupt during
			// the password prompt shuts down instead of waiting for it.
			_, err = hosts.Ensure(hosts.DefaultPath, aliases, hosts.SudoApplierContext(ctx, exe, hosts.DefaultPath))
			return err
		},
		notify: notifyStop,
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

// stopSignals are the signals up shuts down on. The ssh masters run outside
// this process's group, so nothing ends them when the terminal goes away:
// a hangup is therefore handled like Ctrl-C, unless it is being ignored
// (nohup), in which case it stays ignored.
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
	return cmd
}

func runUp(cmd *cobra.Command, b upBackend, flags serveFlags, args []string) error {
	if err := flags.validate(); err != nil {
		return err
	}
	cfg, _, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	names, err := upTargets(cfg, args)
	if err != nil {
		return err
	}
	opts, err := engineOptions(b, flags)
	if err != nil {
		return err
	}
	eng := engine.New(cfg, opts)
	p := &upPrinter{out: cmd.OutOrStdout(), eng: eng, cfg: cfg}

	events, unsubscribe := eng.Events().Subscribe(eventBuffer)
	defer unsubscribe()
	ctx, again, release := b.notify(cmd.Context())
	defer release()
	if err := eng.Start(ctx); err != nil {
		return err
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
	// trust store holds nothing up. The answer is printed once the summary
	// is out, whichever comes first.
	trustHint := make(chan string, 1)
	go func() { trustHint <- untrustedCA(b.trust, opts.StorageRoot) }()
	hint, summarised := "", false

	// Everything is printed from this goroutine, so lines never interleave.
	allStarted := started
	for interrupted := false; !interrupted; {
		select {
		case ev := <-events:
			p.event(ev)
		case hint = <-trustHint:
			trustHint = nil
		case <-allStarted:
			allStarted = nil
			p.buffered(events) // the state changes the summary sums up
			p.summary(names, startErrs)
			p.advise = true // from here a state change is news, and so is its fix
			summarised = true
		case <-ctx.Done():
			interrupted = true
		}
		if summarised && hint != "" {
			fmt.Fprintln(p.out, hint)
			hint = ""
		}
	}

	// A second signal must not end the process: the ssh masters are outside
	// its process group and would be left running. It cuts the shutdown
	// short instead; the engine then gives up on what it was waiting for and
	// still ends every master.
	p.advise = false // what goes wrong while stopping is not for the user to fix
	fmt.Fprintln(p.out, "shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- eng.Shutdown(stopCtx) }()
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
	return nil
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

// upTargets is the projects to start: those named, or every enabled one.
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
		return names, nil
	}
	for _, p := range cfg.Projects {
		if p.Enabled {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no enabled projects to start; name one, like `%s`", upCommand(cfg.Projects[0].Name))
	}
	return names, nil
}

// upCommand is the command line that starts the project called name, fit to
// be copied into a shell: a name that is not one plain word is quoted, and one
// that begins with a dash is kept from being read as a flag.
func upCommand(name string) string {
	plain := name != ""
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-+:@%,=", c)) {
			plain = false
			break
		}
	}
	if !plain {
		name = "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
	}
	if strings.HasPrefix(name, "-") {
		return "nearhost up -- " + name
	}
	return "nearhost up " + name
}

// engineOptions builds the engine's options from the standard directories.
func engineOptions(b upBackend, f serveFlags) (engine.Options, error) {
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
		SSHPath: b.ssh.path, SSHEnv: b.ssh.env,
		HTTPSPort: f.httpsPort, HTTPPort: f.httpPort,
	}
	if !f.noHosts {
		opts.EnsureHosts = askOnce(b.ensureHosts)
	}
	return opts, nil
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
