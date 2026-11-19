package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/update"
)

// updateBackend is the seam between `nearhost update` and the machine.
type updateBackend struct {
	// latest returns the latest released version.
	latest func(ctx context.Context) (string, error)
	// exe is the running binary's path with symlinks resolved.
	exe    func() string
	getenv func(string) string
	// writable reports whether the current user can write to dir.
	writable func(dir string) bool
	// interactive reports whether a prompt can be answered.
	interactive func() bool
	goos        string
	// run runs the update command.
	run func(ctx context.Context, m update.Method, stdout, stderr io.Writer) error
	// download fetches the release of version and writes its nearhost.exe,
	// checked against the release's checksums, to path, a new file beside
	// the running one: the update of a Windows copy that no package manager
	// owns. On failure it leaves nothing at path.
	download func(ctx context.Context, version, path string) error
	// stepAside moves the running binary out of the way, on Windows only,
	// and returns a function that moves it back.
	stepAside func(exe string) (restore func() error, err error)
	// exists reports whether there is a file at path.
	exists func(path string) bool
	// rename moves the file at from to to, waiting out a virus scanner that
	// holds it open for a moment.
	rename func(from, to string) error
	// remove deletes the file at path.
	remove func(path string) error
	// hold catches Ctrl-C and the console closing, which then arrive on
	// signals instead of ending nearhost, until stop is called.
	hold func() (signals <-chan os.Signal, stop func())
	// downloadLimit is the longest the download may take: downloadTimeLimit
	// in production.
	downloadLimit time.Duration
	// probe runs `<path> version` and returns the version it prints, or
	// an error when the binary does not start or fails.
	probe func(ctx context.Context, path string) (string, error)
	// lookPath finds a command on PATH, as exec.LookPath does.
	lookPath func(name string) (string, error)
}

// systemUpdate is the production backend. Only NewRoot wires it in.
func systemUpdate() updateBackend {
	return updateBackend{
		latest: update.Checker{HTTP: updateHTTP(), Current: strings.TrimPrefix(Version, "v"), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}.Latest,
		exe:    realExecutable,
		getenv: os.Getenv,
		writable: func(dir string) bool {
			f, err := os.CreateTemp(dir, ".nearhost-update-*")
			if err != nil {
				return false
			}
			f.Close()
			os.Remove(f.Name())
			return true
		},
		interactive: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
		goos: runtime.GOOS,
		run: func(ctx context.Context, m update.Method, stdout, stderr io.Writer) error {
			c := updateCommand(ctx, m)
			c.Stdout, c.Stderr = stdout, stderr
			return c.Run()
		},
		download:  update.Downloader{HTTP: downloadHTTP()}.Fetch,
		stepAside: update.StepAside,
		exists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		rename: update.MoveIntoPlace,
		remove: os.Remove,
		hold: func() (<-chan os.Signal, func()) {
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
			return signals, func() { signal.Stop(signals) }
		},
		downloadLimit: downloadTimeLimit,
		probe:         probeVersion,
		lookPath:      exec.LookPath,
	}
}

// updateCommand is the command that runs m, found on PATH, with m.Env added
// to the environment. It has the terminal on stdin, for a package manager
// that asks something, except the install script's: piped into sh, the
// installer never had one, so it printed the setcap advice instead of
// asking, and it still does.
func updateCommand(ctx context.Context, m update.Method) *exec.Cmd {
	c := exec.CommandContext(ctx, m.Args[0], m.Args[1:]...)
	c.Env = updateEnv(os.Environ(), m)
	if m.Kind != update.Script {
		c.Stdin = os.Stdin
	}
	return c
}

// probeTimeLimit is the longest `<exe> version` may take after an update.
const probeTimeLimit = 10 * time.Second

// probeVersion runs `<path> version` and returns the version it prints.
func probeVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeLimit)
	defer cancel()
	c := exec.CommandContext(ctx, path, "version")
	c.WaitDelay = time.Second
	out, err := c.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("it gave no answer within %s", probeTimeLimit)
	}
	if err != nil {
		return "", err
	}
	return installedVersion(string(out)), nil
}

// installedVersion is the version in the output of `nearhost version`,
// "nearhost <version>", or the output's first line when it is not that.
func installedVersion(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	line = strings.TrimSpace(line)
	if v, ok := strings.CutPrefix(line, "nearhost "); ok {
		return strings.TrimSpace(v)
	}
	return line
}

// probePath is the nearhost the user's next run starts once m has updated
// exe, the running binary with symlinks resolved, or "" when that cannot
// be told. A Homebrew upgrade installs the new version in a new Caskroom
// folder and links it as <prefix>/bin/nearhost, while the old folder can
// stay. pnpm's store has the version in the path too, so its shim on PATH
// is what runs next. Every other method puts the new binary at exe: the
// install script and go install write to its folder, npm and bun to the
// same package path each version, and the Windows replacement renames the
// new copy to exe.
func probePath(b updateBackend, m update.Method, exe string) string {
	switch m.Kind {
	case update.Homebrew:
		if prefix, _, ok := strings.Cut(exe, "/Caskroom/nearhost/"); ok {
			return prefix + "/bin/nearhost"
		}
		// Not reached: Detect names Homebrew only for a Caskroom path.
		fallthrough
	case update.PNPM:
		if p, err := b.lookPath("nearhost"); err == nil {
			return p
		}
		return ""
	}
	return exe
}

// verifyUpdate checks that the nearhost the next run starts is latest, now
// that the update command m has succeeded. It prints and returns the
// failure: the binary does not start, or it is still another version.
func verifyUpdate(ctx context.Context, cmd *cobra.Command, b updateBackend, m update.Method, exe, latest string) error {
	path := probePath(b, m, exe)
	if path == "" {
		return nil
	}
	byHand := m.String()
	if byHand == "" { // a Windows copy nearhost replaces itself
		byHand = update.InstallScriptWindows
	}
	shown := printable(filepath.Clean(path))
	installed, err := b.probe(ctx, path)
	var msg string
	switch {
	case err != nil:
		msg = fmt.Sprintf("The update ran, but %s did not start: %s. Run it again, or install by hand: %s.", shown, printable(err.Error()), byHand)
	case installed == "":
		msg = fmt.Sprintf("The update ran, but %s printed no version. Run it again, or install by hand: %s.", shown, byHand)
	case !update.SameVersion(installed, latest):
		msg = fmt.Sprintf("The update ran, but %s is still %s. Run it again, or install by hand: %s.", shown, printable(installed), byHand)
		if m.Kind == update.PNPM && shadowsPnpm(b, path) {
			msg += " Another nearhost comes before pnpm's on PATH."
		}
	default:
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), msg)
	return exitError{code: 1, msg: msg}
}

// downloadTimeLimit is the longest the Windows download may take. The
// message for a download that takes longer names it.
const downloadTimeLimit = 5 * time.Minute

// errDownloadSlow and errDownloadInterrupted say why the download's context
// ended.
var (
	errDownloadSlow        = errors.New("the download took longer than 5 minutes")
	errDownloadInterrupted = errors.New("the update was cancelled")
)

func newUpdateCmd(b updateBackend) *cobra.Command {
	var check, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update nearhost to the latest release",
		Long: "Update nearhost to the latest release, the way this copy was installed:\n" +
			"with Homebrew, npm, pnpm, bun, go install or the install script. On\n" +
			"Windows, a copy from the PowerShell installer or a zip is replaced with\n" +
			"the release's nearhost.exe, downloaded from GitHub and checked against\n" +
			"the release's checksums.\n\n" +
			"nearhost also checks for a new release at most once a day and says so\n" +
			"when one is out. Set NEARHOST_NO_UPDATE_CHECK=1 to turn that off.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdateCmd(cmd, b, check, yes)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether an update is available; exit 1 when it is")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "update without asking")
	return cmd
}

func runUpdateCmd(cmd *cobra.Command, b updateBackend, check, yes bool) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	latest, err := b.latest(ctx)
	if err != nil {
		return err
	}
	current := strings.TrimPrefix(Version, "v")
	if !update.Newer(current, latest) {
		if update.IsRelease(current) {
			fmt.Fprintf(out, "nearhost %s is the latest.\n", current)
		} else {
			// On a development build nothing checked that latest, which is
			// GitHub's text, is a version.
			fmt.Fprintf(out, "This is a development build of nearhost (%s). The latest release is %s.\n", Version, printable(latest))
		}
		return nil
	}
	exe := b.exe()
	m := update.DetectOn(b.goos, exe, b.getenv)
	prerelease := update.IsPrerelease(current)
	if prerelease {
		// "latest" never means a pre-release, so name the version.
		m = m.Pinned(latest)
	}
	if check {
		return fmt.Errorf("%s is available (this is %s, installed with %s). Run `nearhost update` to update", latest, current, m.Kind)
	}
	fmt.Fprintf(out, "nearhost %s → %s is available. This copy was installed with %s.\n", current, latest, m.Kind)

	switch {
	case m.Kind == update.Ephemeral:
		tag := "latest"
		if prerelease {
			tag = latest
		}
		fmt.Fprintf(out, "npx fetched this copy for one run. Run `npx @nearhost/cli@%s` to use the latest.\n", tag)
		return nil
	case m.Kind == update.Script && b.goos == "windows" && !b.writable(m.Dir):
		pin := ""
		if v := envOf(m, "NEARHOST_VERSION"); v != "" {
			pin = "$env:NEARHOST_VERSION='" + update.PSQuote(v) + "'; "
		}
		fmt.Fprintf(out, "You cannot write to %s. Open Windows Terminal as administrator and run:\n\n  $env:NEARHOST_INSTALL_DIR='%s'; $env:NEARHOST_NO_PATH='1'; %s%s\n", m.Dir, update.PSQuote(m.Dir), pin, update.InstallScriptWindows)
		return nil
	case m.Kind == update.Script && !b.writable(m.Dir):
		pin := ""
		if v := envOf(m, "NEARHOST_VERSION"); v != "" {
			pin = " NEARHOST_VERSION=" + v
		}
		fmt.Fprintf(out, "You cannot write to %s, so run the installer as root:\n\n  curl -fsSL https://nearhost.dev/install.sh | sudo NEARHOST_INSTALL_DIR=%s%s sh\n", m.Dir, m.Dir, pin)
		return nil
	}

	// A Windows copy from the PowerShell installer or a zip: nearhost
	// downloads the release itself.
	native := m.Kind == update.Script && b.goos == "windows"
	if !yes {
		switch {
		case !b.interactive() && native:
			fmt.Fprintf(out, "Run `nearhost update --yes` to have nearhost download %s from GitHub and replace %s.\n", latest, exe)
			return nil
		case !b.interactive():
			fmt.Fprintf(out, "Run `%s`, or `nearhost update --yes` to have nearhost run it.\n", m)
			return nil
		case native:
			fmt.Fprintf(out, "Download %s from GitHub and replace %s? [Y/n] ", latest, exe)
		default:
			fmt.Fprintf(out, "Run `%s`? [Y/n] ", m)
		}
		answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
		default:
			fmt.Fprintln(out, "Not updated.")
			return nil
		}
	}
	if b.goos != "windows" {
		// The installer installs exactly the version checked below, whatever
		// NEARHOST_VERSION the user's shell exports. The command shown stays
		// the plain one.
		run := m
		if m.Kind == update.Script {
			run = m.Pinned(latest)
		}
		if err := b.run(ctx, run, out, cmd.ErrOrStderr()); err != nil {
			return fmt.Errorf("`%s` failed: %w", m, err)
		}
	} else if err := runAside(ctx, cmd, b, m, exe, latest, native); err != nil {
		return err
	}
	if err := verifyUpdate(ctx, cmd, b, m, exe, latest); err != nil {
		return err
	}
	fmt.Fprintf(out, "Updated to %s.\n", latest)
	if b.goos == "linux" {
		switch m.Kind {
		case update.Go:
			fmt.Fprintf(out, "Let the new binary bind ports 80 and 443 again:\n\n  %s\n", doctor.SetcapFix(exe))
		case update.Homebrew:
			fmt.Fprintln(out, "Let the new binary bind ports 80 and 443 again:\n\n  sudo setcap cap_net_bind_service=+ep \"$(readlink -f \"$(command -v nearhost)\")\"")
		}
	}
	return nil
}

// runAside runs the update on Windows, which will not let anything overwrite
// or delete the running exe: it moves exe out of the way first, and puts it
// back when the update fails, so there is always a working nearhost.
func runAside(ctx context.Context, cmd *cobra.Command, b updateBackend, m update.Method, exe, latest string, native bool) error {
	// A Ctrl-C reaches the installer too, which then fails; closing the
	// console window (SIGTERM to Go) ends the installer and gives nearhost a
	// few seconds. Either way nearhost must not end before the old copy is
	// back, so both are held from before it moves.
	held, stop := b.hold()
	defer stop()
	if native {
		return replaceExe(ctx, b, exe, latest, held)
	}
	restore, err := b.stepAside(exe)
	if err != nil {
		return fmt.Errorf("nothing was updated, as the running nearhost could not be moved out of the way: %w", err)
	}
	failed := func(err error) error {
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("%w, and the old nearhost could not be put back: %v", err, rerr)
		}
		return fmt.Errorf("%w; the old nearhost is back at %s", err, exe)
	}
	if err := b.run(ctx, m, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
		return failed(fmt.Errorf("`%s` failed: %w", m, err))
	}
	if !samePathEveryVersion(m.Kind) || b.exists(exe) {
		return nil
	}
	// A copy renamed from nearhost.exe: `go install` put the new version
	// at <dir>\nearhost.exe, and the renamed one is the old one again.
	if installed := m.Dir + `\nearhost.exe`; m.Kind == update.Go &&
		!strings.EqualFold(installed, exe) && b.exists(installed) {
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("`%s` installed the new nearhost at %s, not at %s, and the old nearhost could not be put back: %v", m, installed, exe, rerr)
		}
		return fmt.Errorf("`%s` installed the new nearhost at %s; %s is still the old one", m, installed, exe)
	}
	return failed(fmt.Errorf("`%s` left no nearhost at %s", m, exe))
}

// replaceExe updates a Windows copy that no package manager owns. It
// downloads the release to <exe>.new and checks it while exe stays where it
// is; only a complete, checked file goes on to the swap: exe moves aside,
// and <exe>.new is renamed to exe. A failed download, a Ctrl-C (held) or
// the time limit during it leaves exe untouched, and the download removes
// <exe>.new. Once
// the swap starts, a Ctrl-C no longer stops it: it finishes, or puts the old
// copy back, so there is always a nearhost at exe.
func replaceExe(ctx context.Context, b updateBackend, exe, latest string, held <-chan os.Signal) error {
	staged := exe + ".new"
	interrupted, interrupt := context.WithCancelCause(ctx)
	defer interrupt(nil)
	dctx, cancel := context.WithTimeoutCause(interrupted, b.downloadLimit, errDownloadSlow)
	defer cancel()
	go func() {
		select {
		case <-held:
			interrupt(errDownloadInterrupted)
		case <-dctx.Done():
		}
	}()
	// download removes what it wrote when it fails. A file at staged then
	// is not this run's: another update running at the same moment may own
	// it, so it stays.
	if err := b.download(dctx, latest, staged); err != nil {
		if cause := context.Cause(dctx); errors.Is(cause, errDownloadSlow) || errors.Is(cause, errDownloadInterrupted) {
			err = cause
		}
		return fmt.Errorf("nearhost %s could not be downloaded: %w; %s is unchanged", latest, err, exe)
	}
	restore, err := b.stepAside(exe)
	if err != nil {
		b.remove(staged)
		return fmt.Errorf("nothing was updated, as the running nearhost could not be moved out of the way: %w", err)
	}
	if err := b.rename(staged, exe); err != nil {
		err = fmt.Errorf("the new nearhost could not be moved to %s: %w", exe, err)
		if rerr := restore(); rerr != nil {
			// The checked new copy stays as a second way back.
			return fmt.Errorf("%w, and the old nearhost could not be put back: %v. The new nearhost is at %s; rename it to %s", err, rerr, staged, exe)
		}
		b.remove(staged)
		return fmt.Errorf("%w; the old nearhost is back at %s", err, exe)
	}
	return nil
}

// samePathEveryVersion reports whether an update of kind puts the new binary
// at the running one's path. pnpm's global store has the version in the
// path, so its new binary is elsewhere and only its exit status counts.
func samePathEveryVersion(k update.Kind) bool {
	switch k {
	case update.Script, update.Go, update.NPM, update.Bun:
		return true
	}
	return false
}

// shadowsPnpm reports whether path, the nearhost found on PATH, is outside
// pnpm's bin folder, so another copy runs before pnpm's. That folder is
// PNPM_HOME, or PNPM_HOME/bin in newer pnpm. It is false when PNPM_HOME is
// not set.
func shadowsPnpm(b updateBackend, path string) bool {
	home := b.getenv("PNPM_HOME")
	if home == "" {
		return false
	}
	dir := path
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		dir = path[:i]
	}
	home = strings.TrimRight(home, `/\`)
	for _, bin := range []string{home, home + "/bin", home + `\bin`} {
		if dir == bin || b.goos == "windows" && strings.EqualFold(dir, bin) {
			return false
		}
	}
	return true
}

// installerVars are the install script's variables that would change what
// it installs or where it downloads from. A script update sets the ones it
// needs in m.Env and drops the rest from the environment it inherits, so a
// variable left exported in the user's shell cannot pin, redirect or
// dry-run the update.
var installerVars = []string{"NEARHOST_VERSION=", "NEARHOST_BASE_URL=", "NEARHOST_INSTALL_DRY_RUN=", "NEARHOST_INSTALL_DIR="}

// updateEnv is the environment the update command m runs with: environ and
// m.Env, without installerVars from environ for the install script.
func updateEnv(environ []string, m update.Method) []string {
	env := slices.Clone(environ)
	if m.Kind == update.Script {
		env = slices.DeleteFunc(env, func(e string) bool {
			return slices.ContainsFunc(installerVars, func(p string) bool { return strings.HasPrefix(e, p) })
		})
	}
	return append(env, m.Env...)
}

// envOf is the value m sets name to, or "". A pre-release build's method
// sets NEARHOST_VERSION.
func envOf(m update.Method, name string) string {
	for _, e := range m.Env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return ""
}

// updateHTTP is the client for update checks, short enough that a slow
// network never holds anything up for long.
func updateHTTP() *http.Client { return &http.Client{Timeout: 3 * time.Second} }

// shouldCheckForUpdate reports whether this run of nearhost checks for a new
// release in the background: not for the commands about versions and help,
// not elevated (as root, or an elevated Windows process), which is the
// hosts helper running with administrator rights, not when stderr is
// not a terminal to say so on, not for a development build, and not when
// NEARHOST_NO_UPDATE_CHECK is set.
func shouldCheckForUpdate(args []string, getenv func(string) string, version string, elevated bool, tty bool) bool {
	if getenv("NEARHOST_NO_UPDATE_CHECK") != "" || elevated || !tty {
		return false
	}
	if !update.IsRelease(version) {
		return false
	}
	for _, a := range args {
		switch a {
		case "update", "version", "help", "completion", "__complete", "--help", "-h", "--version":
			return false
		}
	}
	return true
}

// backgroundCheck is a check for a new release that runs while the command
// does.
type backgroundCheck struct {
	current string
	done    chan struct{}
	latest  string
}

// startBackgroundCheck starts latest in the background.
func startBackgroundCheck(current string, latest func(context.Context) (string, error)) *backgroundCheck {
	c := &backgroundCheck{current: strings.TrimPrefix(current, "v"), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if v, err := latest(ctx); err == nil {
			c.latest = v
		}
	}()
	return c
}

// updateGrace is the longest a command that has finished waits for the
// check. A check answered from the cache is done at once, so only a check
// that asks GitHub waits, and backgroundHTTP gives up within the grace, so
// even a GitHub that does not answer leaves a cache entry for later runs.
const updateGrace = 2 * time.Second

// backgroundHTTP is the background check's client: its timeout is under
// updateGrace, so the check ends, and saves what it found, before the
// command exits.
func backgroundHTTP() *http.Client { return &http.Client{Timeout: 1500 * time.Millisecond} }

// notice is the line saying a new release is out, or "" when there is none
// or the check has not finished within grace. A command that ends quickly
// gives the check a moment, so it can finish and fill the cache; the cached
// answer is then ready at once on later runs.
func (c *backgroundCheck) notice(grace time.Duration) string {
	if c == nil {
		return ""
	}
	select {
	case <-c.done:
	default:
		select {
		case <-c.done:
		case <-time.After(grace):
			return ""
		}
	}
	if !update.Newer(c.current, c.latest) {
		return ""
	}
	return fmt.Sprintf("A new nearhost is available: %s → %s. Run `nearhost update` to update.", c.current, c.latest)
}

// wait is the notice once the check has finished, on a channel the
// interface can wait on without holding anything up.
func (c *backgroundCheck) wait() <-chan string {
	ch := make(chan string, 1)
	if c == nil {
		close(ch)
		return ch
	}
	go func() {
		<-c.done
		if n := c.notice(0); n != "" {
			ch <- n
		}
		close(ch)
	}()
	return ch
}

// cachedLatest is the background check's source: GitHub, asked at most once
// a day.
func cachedLatest(ctx context.Context) (string, error) {
	path, err := update.CachePath()
	if err != nil {
		return "", err
	}
	return update.Checker{HTTP: backgroundHTTP(), CachePath: path, Current: strings.TrimPrefix(Version, "v"), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}.Cached(ctx)
}

// downloadHTTP is the client for the Windows download. It has no overall
// timeout, which would cut off a slow download that is still going (the
// download's context has downloadTimeLimit), but it gives up on a server
// that does not connect or answer.
func downloadHTTP() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: tr}
}
