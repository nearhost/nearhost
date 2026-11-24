package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/desktop"
	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/transport"
	"github.com/nearhost/nearhost/internal/tui"
)

// uiBackend is the seam between bare `nearhost` and the machine.
type uiBackend struct {
	up upBackend
	// interactive reports whether standard input and output are terminals.
	interactive func() bool
	// run runs the interface until it quits, calling attach with the
	// program that runs m before it starts.
	run func(m *tui.Model, attach func(*tea.Program)) error
	// applier is the privileged hosts write, given the context of the start
	// that needs it; prompt says whether sudo may prompt (only in the
	// terminal).
	applier func(ctx context.Context, prompt bool) hosts.Applier
	// root reaches sudo, for steps that need root.
	root rootBackend
	// readHosts returns the content of the hosts file.
	readHosts func() (string, error)
	// lookup finds the addresses DNS gives a name, for the hosts state; nil
	// keeps every name.
	lookup hosts.Lookup
	// open opens a URL in the browser.
	open func(url string) error
	// workDir is the directory nearhost was started in.
	workDir func() (string, error)
	// presets reaches the trusted preset registries.
	presets presetBackend
	// sshHosts lists the host names in the user's OpenSSH client config and
	// the files it includes.
	sshHosts func() ([]string, error)
	// stops listens, from the call until the release, for the signals
	// that ask the process to stop. Each one the interface gets is
	// handled as Ctrl-C: the first quits, the next cuts the shutdown
	// short.
	stops func() (sigs <-chan os.Signal, release func())
	// rootMade, when set, is given the interface's way to run steps as
	// root. Tests use it to lend the terminal; it is nil otherwise.
	rootMade func(asRoot)
	// detach reaches the core, and starts it in the background when none
	// runs, for bare `nearhost` and `nearhost -d`.
	detach detachBackend
	// clientMade, when set, is given the interface's backend as soon as it
	// is made, before anything is published on its bus. Tests use it to
	// listen; it is nil otherwise.
	clientMade func(*clientBackend)
}

// systemUI is the production backend. Only NewRoot wires it in, with the
// lookup the engine, the hosts state and doctor share.
func systemUI(lookup hosts.Lookup) uiBackend {
	return uiBackend{
		up: systemUp(lookup),
		interactive: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
		run: func(m *tui.Model, attach func(*tea.Program)) error {
			// runUI handles the stop signals, through the model's own
			// quit, so the program must not end on them by itself.
			p := tea.NewProgram(m, tea.WithoutSignalHandler())
			attach(p)
			_, err := p.Run()
			return err
		},
		applier: func(ctx context.Context, prompt bool) hosts.Applier {
			exe, err := os.Executable()
			if err != nil {
				return func([]string) error { return err }
			}
			// The applier ends sudo when ctx does, so a project stopped
			// during the password prompt stops waiting for it.
			return hosts.ElevatedApplier(ctx, elevate.System(), exe, hosts.DefaultPath, terminalPrompt, prompt)
		},
		root: systemRoot(elevate.System()),
		readHosts: func() (string, error) {
			data, err := os.ReadFile(hosts.DefaultPath)
			return string(data), err
		},
		lookup:   lookup,
		open:     desktop.OpenURL,
		workDir:  os.Getwd,
		presets:  systemPresets(),
		sshHosts: transport.DefaultHosts,
		stops:    notifyUIStop,
		detach:   systemDetach(),
	}
}

// notifyUIStop is the stops of the production backend. The ssh masters
// run outside this process's group, so a closed terminal must quit the
// interface as Ctrl-C does rather than end the process on the spot.
func notifyUIStop() (<-chan os.Signal, func()) {
	// While this is registered no stop signal ends the process: those that
	// do not fit are dropped, which leaves one to quit and one to hurry.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, stopSignals()...)
	return sigs, func() { signal.Stop(sigs) }
}

// ctrlC is the key the interface quits on, and what a stop signal is
// handed to it as.
var ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// stopForwarder hands the stop signals to the interface as Ctrl-C.
type stopForwarder struct {
	// lent is set while the interface has lent the terminal to a step,
	// such as sudo's password prompt.
	lent atomic.Bool
}

// suspend wraps run, the interface's way to lend the terminal, so that f
// knows when a step has it.
func (f *stopForwarder) suspend(run func(context.Context, func(io.Reader, io.Writer) error) error) func(context.Context, func(io.Reader, io.Writer) error) error {
	return func(ctx context.Context, fn func(io.Reader, io.Writer) error) error {
		return run(ctx, func(in io.Reader, out io.Writer) error {
			f.lent.Store(true)
			defer f.lent.Store(false)
			return fn(in, out)
		})
	}
}

// forward hands each signal on sigs to p as Ctrl-C until done is closed.
// An interrupt while a step has the terminal is the user's Ctrl-C at that
// step, which shares this process's group: it ends the step and is not
// handed on. A hangup or terminate then waits for the step to end, and
// quits.
func (f *stopForwarder) forward(p *tea.Program, sigs <-chan os.Signal, done <-chan struct{}) {
	for {
		select {
		case sig := <-sigs:
			if sig == os.Interrupt && f.lent.Load() {
				continue
			}
			p.Send(ctrlC)
		case <-done:
			return
		}
	}
}

// bindUI makes bare `nearhost` open the interface at a terminal, with the
// serving flags of `up`, and fail with a pointer to -d and `up` otherwise.
// With -d it starts the core in the background instead, which needs no
// terminal.
func bindUI(root *cobra.Command, b uiBackend) {
	var flags serveFlags
	var detach bool
	flags.bind(root)
	flags.bindCapture(root)
	root.Flags().BoolVarP(&detach, "detach", "d", false, "start Nearhost in the background, with no interface, and return once it answers")
	root.Args = rootArgs
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if detach {
			return runDetach(cmd, b.detach, flags)
		}
		if b.interactive == nil || !b.interactive() {
			return noTerminal(cmd.ErrOrStderr())
		}
		return runUI(cmd, b, flags)
	}
}

// noTerminal prints that the interface needs a terminal, which is standard
// input and output both being one, and returns the exit status 1 for Main.
// The message is printed here so that Main adds no "nearhost: " prefix.
func noTerminal(stderr io.Writer) error {
	msg := "nearhost needs a terminal for its interface. Use nearhost -d to serve in the background, or nearhost up to serve without one."
	if hostOS == "windows" {
		msg += " In Git Bash, run it through winpty or use Windows Terminal."
	}
	fmt.Fprintln(stderr, msg)
	return exitError{code: 1, msg: msg}
}

// rootArgs is cobra.NoArgs, except that a single argument that looks like a
// mistyped command is answered with the commands it may have meant.
func rootArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
			return fmt.Errorf("unknown command %q for %q\n\nDid you mean this?\n\t%s", args[0], cmd.CommandPath(), strings.Join(s, "\n\t"))
		}
	}
	return cobra.NoArgs(cmd, args)
}

// runUI is bare `nearhost`: it opens the interface as a client of the core,
// which it starts in the background first when none runs. The interface's
// own work stays in this process: the terminal, the hosts file, the trust
// store and the config file it edits.
func runUI(cmd *cobra.Command, b uiBackend, flags serveFlags) error {
	if err := flags.validate(); err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	errOut := cmd.ErrOrStderr()
	// A config that does not load is refused here, before a core is
	// started on it only to stop.
	if _, _, ok := detachInfo(ctx, b.detach); !ok {
		if _, _, err := loadConfig(cmd); err != nil {
			return err
		}
	}
	dir, err := b.workDir()
	if err != nil {
		return err
	}
	c, info, spawned, err := ensureInterruptible(cmd, b, func() (*control.Client, control.Info, bool, error) {
		return ensureCore(cmd, b.detach, flags, control.StartedTUI)
	})
	if err != nil {
		return err
	}
	if !spawned {
		// The core is checked before it is offered a restart: a restart
		// must not replace a core serving another file.
		if err := knownAPI(cmd, info); err != nil {
			return err
		}
		st, err := c.State(ctx)
		if err != nil {
			return fmt.Errorf("ask Nearhost in the background what it serves: %w", err)
		}
		if err := sameConfig(cmd, st.ConfigPath); err != nil {
			return err
		}
		if c, info, spawned, err = offerRestart(cmd, b, c, info, st); err != nil {
			return err
		}
	}
	// The core is the interface's only when it was started for it: one the
	// service started, though for this interface, is the service's, and is
	// left running as a core found running is.
	owned := spawned && info.StartedBy == control.StartedTUI
	// A core started for this interface is stopped should the interface not
	// take over: no interface would follow it.
	abandon := func() {
		if !owned {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), detachTimeout)
		defer cancel()
		if err := c.Stop(ctx); err == nil && b.detach.gone != nil {
			_ = b.detach.gone(ctx)
		}
	}
	st, err := c.State(ctx)
	if err != nil {
		abandon()
		return fmt.Errorf("ask Nearhost in the background what it serves: %w", err)
	}
	// What the core writes to its log from here on says whether it was
	// asked to stop, should it go while the interface runs.
	logFrom := b.detach.logSize()
	storage, err := config.CaddyDir()
	if err != nil {
		abandon()
		return err
	}
	var suspender tui.Suspender
	var stops stopForwarder
	root := asRoot{sudo: b.root, ask: suspender.Password, suspend: stops.suspend(suspender.Run), width: terminalWidth, turn: make(chan struct{}, 1)}
	// Every sudo the interface runs, the CA's included, which cannot be
	// given a prompt of its own, words its prompt as Nearhost's.
	os.Setenv("SUDO_PROMPT", terminalPrompt)
	if b.rootMade != nil {
		b.rootMade(root)
	}
	var proposals tui.Proposals
	// Whether the hosts file is managed is the core's to say: --no-hosts
	// given to an interface that found the core running is ignored, as the
	// note says, and one the core was started with is shown.
	localFlags := flags
	localFlags.noHosts = st.NoHosts
	local := &localOps{b: b, path: st.ConfigPath, flags: localFlags, storage: storage, root: root}
	back := newClientBackend(c, st, local, &proposals, owned)
	back.gone = b.detach.gone
	defer back.Close()
	if b.clientMade != nil {
		b.clientMade(back)
	}
	// From here a closed terminal or a kill quits the interface, which
	// stops the core or leaves it as a quit does.
	var sigs <-chan os.Signal
	if b.stops != nil {
		var release func()
		sigs, release = b.stops()
		defer release()
	}
	running := make(chan struct{})
	defer close(running)
	m := tui.New(back, dir)
	// The interface has subscribed: what is published from here on is in
	// its log, before anything from the core, which it follows once it
	// starts.
	if !spawned {
		if note := ignoredFlags(cmd, st); note != "" {
			back.bus.Publish(engine.Event{Message: note})
		}
	}
	err = b.run(m, func(p *tea.Program) {
		suspender.Attach(p)
		proposals.Attach(p)
		if sigs != nil {
			go stops.forward(p, sigs, running)
		}
	})
	back.Close()
	if m.Err() != nil {
		return coreLost(errOut, b.detach, logFrom, info.StartedBy)
	}
	if err != nil && !back.quit() {
		abandon()
	}
	// Said once the terminal is the shell's again. A core in a terminal
	// shows there that it runs.
	if err == nil && back.left() && info.StartedBy != control.StartedForeground {
		fmt.Fprintln(cmd.OutOrStdout(), "Nearhost is still running in the background. Stop it with nearhost stop.")
	}
	return err
}

// ensureInterruptible runs start, ensureCore or startCore for the interface,
// with the stop signals caught while it waits: one ends the wait, and the
// core started here for the interface, which no interface would follow, is
// stopped. The process then exits as an interrupted command does.
func ensureInterruptible(cmd *cobra.Command, b uiBackend, start func() (*control.Client, control.Info, bool, error)) (*control.Client, control.Info, bool, error) {
	if b.stops == nil {
		return start()
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, interrupt := context.WithCancel(parent)
	defer interrupt()
	sigs, release := b.stops()
	defer release()
	done, caught := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(caught)
		select {
		case <-sigs:
			interrupt()
		case <-done:
		}
	}()
	cmd.SetContext(ctx)
	c, info, spawned, err := start()
	cmd.SetContext(parent)
	close(done)
	<-caught
	if err != nil && ctx.Err() != nil && parent.Err() == nil {
		var fate spawnedFate
		if ie := (interruptedStart{}); errors.As(err, &ie) {
			fate = ie.fate
		}
		msg := interruptedMessage(fate)
		fmt.Fprintln(cmd.ErrOrStderr(), msg)
		return nil, control.Info{}, false, exitError{code: interruptedCode, msg: msg}
	}
	return c, info, spawned, err
}

// interruptedMessage says that the start was interrupted, and what became of
// the core: fate.
func interruptedMessage(fate spawnedFate) string {
	const interrupted = "Interrupted while Nearhost started in the background"
	switch fate {
	case fateStopped:
		return interrupted + ", so it was stopped."
	case fateExited:
		return interrupted + ". It stopped before it answered."
	case fateOther:
		return interrupted + ". Another nearhost started the Nearhost that runs, so it was left running."
	case fateNone:
		return "Interrupted before Nearhost was started in the background, so it is not running."
	case fateStopping:
		return interrupted + ". It was asked to stop and has yet to finish; nearhost stop waits until it has."
	case fateNoAnswer:
		path, _ := config.LogPath()
		return interrupted + ". It did not answer in time to be stopped, so it may still be starting: see " + path + ", and stop it with nearhost stop."
	}
	return interrupted + ". It goes on starting; stop it with nearhost stop."
}

// knownAPI refuses a core that speaks another version of the API, which
// this nearhost cannot drive.
func knownAPI(cmd *cobra.Command, info control.Info) error {
	if info.API == control.APIVersion {
		return nil
	}
	msg := "Nearhost in the background speaks a different version of its API. Run nearhost stop, then nearhost."
	fmt.Fprintln(cmd.ErrOrStderr(), msg)
	return exitError{code: 1, msg: msg}
}

// offerRestart offers, at the terminal, to restart the core c, which says
// info of itself and serves as st says, when it is of another release, as
// after `nearhost update`. Restarted, the core is stopped and this release's
// is started as the old one was: on its config file and ports, and said to
// be started by what started the old one. c is then the new core's, and
// restarted says the interface started it, as it did the old one. A core
// that runs in a terminal is not offered a restart: it was started by hand,
// and only there can it be started again. info is what the core followed
// from here on says of itself. A core the installed service started is
// restarted through the service, and is not offered a restart when the
// service runs another copy of nearhost, which would start it again as it
// was. With no service to read, it is started directly, and then says it was
// started detached, as the service manager did not start it.
func offerRestart(cmd *cobra.Command, b uiBackend, c *control.Client, info control.Info, st control.State) (_ *control.Client, _ control.Info, restarted bool, err error) {
	if info.Version == Version {
		return c, info, false, nil
	}
	if info.StartedBy == control.StartedForeground {
		fmt.Fprintf(cmd.OutOrStdout(), "Nearhost running in a terminal is %s; this is %s. It was started by hand, so restart it there: stop it with Ctrl-C or nearhost stop, then start it again.\n", printable(info.Version), Version)
		return c, info, false, nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	detach := b.detach
	viaService := false
	if info.StartedBy == control.StartedService {
		var m service.Manager
		var d service.Definition
		detach, m, d = detach.withService(ctx)
		if m != nil {
			if !samePath(d.Exe, detach.exe()) {
				sayServiceRunsAnother(cmd.OutOrStdout(), d.Exe, info.Version, Version)
				return c, info, false, nil
			}
			viaService = true
		}
	}
	if !askRestart(cmd.InOrStdin(), cmd.OutOrStdout(), info.Version) {
		return c, info, false, nil
	}
	by := info.StartedBy
	if by == "" {
		// A core that does not say is not taken for the interface's.
		by = control.StartedDetached
	}
	c, info, spawned, err := stopAndStart(cmd, detach, c, st, "", by, viaService, func(start func() (*control.Client, control.Info, bool, error)) (*control.Client, control.Info, bool, error) {
		return ensureInterruptible(cmd, b, start)
	})
	// Only a core started for the interface is the interface's: the
	// service's is not, nor one started as -d's.
	return c, info, spawned && info.StartedBy == control.StartedTUI, err
}

// askRestart asks at the terminal whether to restart the core, which is the
// release old, and reports the answer: yes or just enter restarts it, no
// leaves it, and anything else is asked again. Input that ends unanswered
// leaves it.
func askRestart(in io.Reader, out io.Writer, old string) bool {
	return askYesNo(in, out, fmt.Sprintf("Nearhost in the background is %s; this is %s. Restart it now? [Y/n] ", printable(old), Version))
}

// sameConfig refuses a --config that names another file than served, the
// one the running core serves: the interface edits the core's file.
func sameConfig(cmd *cobra.Command, served string) error {
	if !cmd.Flags().Changed("config") {
		return nil
	}
	given, err := cmd.Flags().GetString("config")
	if err != nil || given == "" {
		return err
	}
	if given, err = filepath.Abs(given); err != nil {
		return err
	}
	if samePath(given, served) {
		return nil
	}
	msg := fmt.Sprintf("Nearhost in the background serves %s. Stop it with nearhost stop to use %s, or open nearhost without --config.", printable(served), printable(given))
	fmt.Fprintln(cmd.ErrOrStderr(), msg)
	return exitError{code: 1, msg: msg}
}

// samePath reports whether a and b name the same file: the same path, or
// two paths to one file, as through a macOS temporary folder's symlink.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// ignoredFlags is the note that the serve flags given on cmd do nothing to
// the core already running, which serves as st says; "" when none was
// given, or each port given is the one the core has.
func ignoredFlags(cmd *cobra.Command, st control.State) string {
	ports := map[string]int{"https-port": st.HTTPSPort, "http-port": st.HTTPPort, "web-port": st.WebPort}
	var names []string
	for _, name := range detachServeFlags {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		if port, ok := ports[name]; ok && f.Value.String() == strconv.Itoa(port) {
			continue
		}
		names = append(names, "--"+name)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " applies only when Nearhost starts; the running Nearhost keeps its settings."
	}
	list := strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	return list + " apply only when Nearhost starts; the running Nearhost keeps its settings."
}

// coreLost prints that the core went while the interface ran, with the end
// of its log, where it said why, unless what it wrote since from says it was
// asked to stop. A core that ran in a terminal, which by says, had the
// terminal for its log, so none is shown. It returns the exit status 1 for
// Main.
func coreLost(w io.Writer, d detachBackend, from int64, by control.StartedBy) error {
	if by == control.StartedForeground {
		// Its terminal was its log.
		msg := "Nearhost stopped. It ran in a terminal, which shows why."
		fmt.Fprintln(w, msg)
		return exitError{code: 1, msg: msg}
	}
	msg := "Nearhost in the background stopped."
	if !stoppedOnRequest(d.logTail(from, lostLogLines)) {
		if tail := d.logTail(0, detachTailLines); len(tail) > 0 {
			path, _ := config.LogPath()
			msg += " The end of " + path + ":\n" + printableLines(strings.Join(tail, "\n"))
		}
	}
	fmt.Fprintln(w, msg)
	return exitError{code: 1, msg: msg}
}

// lostLogLines is how many of the lines a lost core wrote are looked at for
// its shutdown: more than a shutdown writes after it.
const lostLogLines = 1000

// stoppedOnRequest reports whether the core's log lines say it began to shut
// down, as it does when stopped through `nearhost stop` or a signal, rather
// than ending some other way. Its line is its stamp and "shutting down"
// alone: a project's output may say the same words after its own name.
func stoppedOnRequest(lines []string) bool {
	for _, l := range lines {
		stamp, rest, ok := strings.Cut(l, " ")
		if !ok || rest != "shutting down" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, stamp); err == nil {
			return true
		}
	}
	return false
}

// hostsThrough brings the hosts block up to date for the interface: when the
// block has to hold other names than it does, block, it runs the privileged
// write as root, asking for the password in the interface when sudo needs it
// (on Windows, Windows asks). aliases are the names of the projects that wait
// on it, which the box names.
func hostsThrough(root asRoot, path string, applier func(ctx context.Context, prompt bool) hosts.Applier) func(ctx context.Context, aliases, block []string) error {
	return func(ctx context.Context, aliases, block []string) error {
		_, err := hosts.Ensure(path, block, func(want []string) error {
			// A start that has ended since it asked needs no password.
			if err := ctx.Err(); err != nil {
				return err
			}
			// The message names the projects' names alone: want adds
			// Nearhost's page to them, which is never the reason to ask.
			names := strings.Join(aliases, ", ")
			why := fmt.Sprintf("Nearhost needs your password to add %s to %s.", names, path)
			err := root.run(ctx, why, "Added to "+path+".", func(ctx context.Context, terminal io.Writer) error {
				return applier(ctx, terminal != nil)(want)
			})
			verb := "were"
			if len(aliases) == 1 {
				verb = "was"
			}
			if errors.Is(err, tui.ErrPasswordCancelled) {
				return fmt.Errorf("you cancelled the password, so %s %s not added to %s", names, verb, path)
			}
			if errors.Is(err, elevate.ErrDeclined) {
				return fmt.Errorf("you declined the Windows prompt, so %s %s not added to %s", names, verb, path)
			}
			return err
		})
		return err
	}
}
