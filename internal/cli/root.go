// Package cli defines the nearhost command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/report"
	rterm "github.com/nearhost/nearhost/internal/report/term"
	"github.com/nearhost/nearhost/internal/update"
)

// dnsTTL is how long a DNS answer about a host is used, and dnsFailTTL how
// long a failed lookup is, before the name is looked up again. Failures
// expire sooner so a nameserver that comes back is noticed.
const (
	dnsTTL     = time.Minute
	dnsFailTTL = 10 * time.Second
)

// Version is set at build time by the release tooling. A build without it,
// such as `go install …@latest`, takes its module version (see
// buildVersion).
var Version = "dev"

func init() { Version = buildVersion(Version, debug.ReadBuildInfo) }

// pseudoVersion matches the end of a Go pseudo-version, such as
// v0.13.1-0.20261118120000-abcdef123456: a commit that is not a release.
var pseudoVersion = regexp.MustCompile(`(^|[-.])\d{14}-[0-9a-f]{12}$`)

// buildVersion is version, the release tooling's value, or for a build
// without one ("dev") the module version Go recorded, without its v: a
// `go install …@v0.6.0` or `@latest` build records v0.6.0, which is that
// release's code. A local build records (devel), a pseudo-version or a
// +dirty version, and stays dev.
func buildVersion(version string, read func() (*debug.BuildInfo, bool)) string {
	if version != "dev" {
		return version
	}
	info, ok := read()
	if !ok || info == nil {
		return version
	}
	v := info.Main.Version
	if !strings.HasPrefix(v, "v") || strings.Contains(v, "+") || pseudoVersion.MatchString(v) || !update.IsRelease(v) {
		return version
	}
	return strings.TrimPrefix(v, "v")
}

// NewRoot builds the root command and all subcommands. It is the only place
// where the commands are wired to the real machine.
func NewRoot() *cobra.Command {
	// One cache of DNS answers serves the engine, the interface's hosts
	// state and doctor. The interface checks the hosts file every second,
	// and a cache of its own per caller would still ask DNS that often.
	lookup := hosts.CachedLookup(hosts.DNSLookup, dnsTTL, dnsFailTTL)
	root := newRootCmd()
	root.AddCommand(
		newVersionCmd(),
		newHostsCmd(lookup),
		newTrustCmd(systemTrust()),
		newUntrustCmd(systemTrust()),
		newUpCmd(systemUp(lookup)),
		newDaemonCmd(systemUp(lookup)),
		newStopCmd(),
		newStatusCmd(systemService().manager),
		newServiceCmd(systemService()),
		newAddCmd(systemAdd()),
		newExportCmd(systemExport()),
		newLsCmd(),
		newPresetCmd(systemPresets()),
		newDoctorCmd(systemDoctor(lookup)),
		newUpdateCmd(systemUpdate()),
	)
	bindUI(root, systemUI(lookup))
	// Cobra adds completion, a group of its own, when the command runs.
	root.InitDefaultCompletionCmd()
	if c, _, err := root.Find([]string{"completion"}); err == nil && c != root {
		c.RunE = groupRun
	}
	return root
}

// newRootCmd is the root command with its shared flags and no subcommands.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "nearhost",
		Short: "Reach local and remote dev servers at their own domain (https://<name>.test by default)",
		Long: `Reach local and remote dev servers at their own domain (https://<name>.test by default).

Getting started:
  nearhost add .     Add the project in this directory
  nearhost           Open the interface, which serves every enabled project
  nearhost up        Serve them without the interface
  nearhost -d        Serve them in the background
  nearhost status    Show what the background Nearhost is serving
  nearhost stop      Stop it
  nearhost doctor    Check the machine and each project

The interface needs a terminal. Without a terminal, nearhost prints that it
needs one and exits 1; use nearhost -d or nearhost up there.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SuggestionsMinimumDistance = 2
	// A mistyped command with a flag, as in `nearhost lss --foo`, fails on
	// the flag first; the suggestion is the more useful thing to say.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if !cmd.HasParent() {
			if args := cmd.Flags().Args(); len(args) == 1 && len(cmd.SuggestionsFor(args[0])) > 0 {
				return rootArgs(cmd, args)
			}
		}
		return err
	})
	root.SetVersionTemplate("nearhost {{.Version}}\n")
	root.PersistentFlags().String("config", "", configFlagUsage)
	root.PersistentFlags().Bool("verbose", false, "with an error, warning or note, also show its code and what caused it")
	// Every command's warnings and notes, and what code deep down reports,
	// go to its standard error through the context.
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) { setSink(cmd, commandSink(cmd)) }
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nearhost version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "nearhost %s\n", Version)
			return err
		},
	}
}

// defaultWebPort is where Nearhost's page is served,
// https://nearhost.localhost:6327: NEAR on a phone keypad.
const defaultWebPort = 6327

// serveFlags are shared by up and doctor.
type serveFlags struct {
	httpsPort int
	httpPort  int
	webPort   int // 0 serves no web port
	noHosts   bool
	noCapture bool
}

func (f *serveFlags) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.httpsPort, "https-port", 443, "port for https://<alias>")
	cmd.Flags().IntVar(&f.httpPort, "http-port", 80, "port that redirects to https")
	cmd.Flags().IntVar(&f.webPort, "web-port", defaultWebPort, "port for Nearhost's page, at https://nearhost.localhost:<port>; 0 for no page")
	cmd.Flags().BoolVar(&f.noHosts, "no-hosts", false, "do not manage "+hosts.DefaultPath)
}

// bindCapture adds --no-capture, for the commands that serve.
func (f *serveFlags) bindCapture(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.noCapture, "no-capture", false, "do not keep requests and responses in memory")
}

func (f *serveFlags) validate() error {
	for _, p := range []struct {
		flag string
		port int
	}{{"--https-port", f.httpsPort}, {"--http-port", f.httpPort}} {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535, not %d", p.flag, p.port)
		}
	}
	if f.httpsPort == f.httpPort {
		return fmt.Errorf("--https-port and --http-port must differ (both are %d)", f.httpPort)
	}
	if f.webPort < 0 || f.webPort > 65535 {
		return fmt.Errorf("--web-port must be between 1 and 65535, or 0 for none, not %d", f.webPort)
	}
	if f.webPort == f.httpsPort || f.webPort == f.httpPort {
		return fmt.Errorf("--web-port must differ from --https-port and --http-port (it is %d)", f.webPort)
	}
	return nil
}

// configPath resolves --config or the default location.
func configPath(cmd *cobra.Command) (string, error) {
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return "", err
	}
	if path != "" {
		return path, nil
	}
	return config.DefaultPath()
}

// missingConfigMessage is the "no projects" line for a config file named with
// --config that does not exist, or "" when it does not apply: no --config was
// given (the default file is missing on a fresh install, which is no mistake),
// or the file exists, or cannot be read for another reason, which loadConfig
// reports. The path is made absolute, so a relative path that resolves from the
// wrong directory shows where Nearhost looked. `nearhost add` creates the file.
func missingConfigMessage(cmd *cobra.Command) string {
	given, err := cmd.Flags().GetString("config")
	if err != nil || given == "" {
		return ""
	}
	if _, err := os.Stat(given); !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	path, err := filepath.Abs(given)
	if err != nil {
		path = filepath.Clean(given)
	}
	return "No projects in " + printable(path) + " (the file does not exist yet)."
}

// loadConfig loads and validates the config and returns it with its path. A
// missing file is an empty config.
func loadConfig(cmd *cobra.Command) (*config.Config, string, error) {
	path, err := configPath(cmd)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, path, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, path, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, path, nil
}

// Main runs the CLI with the given arguments and returns the process exit code.
func Main(args []string) int {
	startReporting()
	tty := term.IsTerminal(int(os.Stderr.Fd()))
	elevated := elevate.Elevated()
	clearOldCopies(elevated)
	root := NewRoot()
	if shouldCheckForUpdate(root, args, os.Getenv, Version, elevated, tty) {
		updates = startBackgroundCheck(Version, cachedLatest)
	}
	root.SetArgs(args)
	verbose := func() bool { v, _ := root.PersistentFlags().GetBool("verbose"); return v }
	code := exitCodeVerbose(root.Execute(), os.Stderr, verbose())
	if n := updates.notice(updateGrace); n != "" {
		rterm.Render(os.Stderr, report.Notef(report.UpdateAvailable, "%s", n), rterm.For(os.Stderr, verbose()))
	}
	return code
}

// startedReporting makes startReporting's work happen once a process.
var startedReporting sync.Once

// startReporting sets what the report package needs from the process: the
// version and the folder crash files go to, which is the core log's, and a
// fallback for messages reported where no sink is set, which goes to the
// core log once a core in the background has opened it (see coreFallback)
// and to standard error until then. It runs before any goroutine starts.
func startReporting() {
	startedReporting.Do(func() {
		report.Version = Version
		if path, err := config.LogPath(); err == nil {
			report.CrashDir = filepath.Dir(path)
		}
		stderr := report.Fallback
		report.Fallback = report.SinkFunc(func(m *report.Message) {
			if h := coreFallback.Load(); h != nil {
				h.Report(m)
				return
			}
			if stderr != nil {
				stderr.Report(m)
			}
		})
	})
}

// sinkHolder holds a sink, for an atomic pointer.
type sinkHolder struct{ report.Sink }

// coreFallback is the core log's sink while a core in the background runs
// in this process, and nil otherwise.
var coreFallback atomic.Pointer[sinkHolder]

// sinkKey is the context key of the sink a command reports to.
type sinkKey struct{}

// setSink makes cmd's context carry s, for report.To and sinkOf.
func setSink(cmd *cobra.Command, s report.Sink) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cmd.SetContext(context.WithValue(report.WithSink(ctx, s), sinkKey{}, s))
}

// sinkOf is the sink cmd reports to: its context's, or for a command run
// without the root's PersistentPreRun, as some tests run them, its
// commandSink.
func sinkOf(cmd *cobra.Command) report.Sink {
	if ctx := cmd.Context(); ctx != nil {
		if s, ok := ctx.Value(sinkKey{}).(report.Sink); ok {
			return s
		}
	}
	return commandSink(cmd)
}

// commandSink renders what it is given for a terminal on cmd's standard
// error, as it is when the message is reported: a core in the background
// points that at its log. --verbose adds codes and causes.
func commandSink(cmd *cobra.Command) report.Sink {
	return report.SinkFunc(func(m *report.Message) {
		w := cmd.ErrOrStderr()
		rterm.Render(w, m, termOptions(w, verboseFlag(cmd)))
	})
}

// verboseFlag reports whether --verbose was given to cmd.
func verboseFlag(cmd *cobra.Command) bool {
	f := cmd.Flag("verbose")
	if f == nil {
		return false
	}
	v, _ := strconv.ParseBool(f.Value.String())
	return v
}

// termOptions are the options for rendering to w: a terminal's width and
// colour when w is one.
func termOptions(w io.Writer, verbose bool) rterm.Options {
	if f, ok := w.(*os.File); ok {
		return rterm.For(f, verbose)
	}
	return rterm.Options{Verbose: verbose}
}

// alreadySaid is an error that has already been said in full where its
// reader looks: the end of doctor's report, up's summary, or the log of a
// core in the background, which is also its standard error. Main exits
// with its message's status and prints nothing more.
type alreadySaid struct{ err error }

func (e alreadySaid) Error() string { return e.err.Error() }
func (e alreadySaid) Unwrap() error { return e.err }

// exitCode is exitCodeVerbose without --verbose.
func exitCode(err error, stderr io.Writer) int { return exitCodeVerbose(err, stderr, false) }

// exitCodeVerbose is the exit code for the error a command returned, which
// is 0 for nil: the status its message sets, or 1. It renders the error
// for a terminal on stderr, with its code and causes when verbose, unless
// it was already said (see alreadySaid). Every text in it is made printable
// by the renderer: an error can quote a project's file, a server or ssh.
func exitCodeVerbose(err error, stderr io.Writer, verbose bool) int {
	if err == nil {
		return 0
	}
	m := messageOf(err)
	var said alreadySaid
	if !errors.As(err, &said) {
		rterm.Render(stderr, m, termOptions(stderr, verbose))
	}
	if m.Exit != 0 {
		return m.Exit
	}
	return 1
}

// messageOf is report.From, except that an error with no message whose
// text runs over several lines, as a usage error or a list of problems
// does, keeps its first line as the title and the rest as the explanation,
// rather than one line run together.
func messageOf(err error) *report.Message {
	m := report.From(err)
	if m.Code == report.Unexpected {
		if title, rest, ok := strings.Cut(strings.TrimSpace(m.Title), "\n"); ok {
			m.Title, m.Why = title, strings.TrimSpace(rest)
		}
	}
	return m
}

// cleanOld deletes the copies earlier updates moved aside. Tests of Main
// replace it, so they leave the real %LOCALAPPDATA% alone.
var cleanOld = update.CleanOld

// clearingOld counts the cleanOld runs clearOldCopies has started and not
// finished. Tests wait on it before they restore cleanOld.
var clearingOld sync.WaitGroup

// clearOldCopies starts cleanOld in the background, so no command waits on
// it, and reports whether it did. An elevated run, as the hosts helper is,
// leaves it to the user's own runs. It does nothing off Windows.
func clearOldCopies(elevated bool) bool {
	if elevated {
		return false
	}
	clearingOld.Add(1)
	go func() {
		defer clearingOld.Done()
		cleanOld(realExecutable())
	}()
	return true
}

// updates is this run's check for a new release, or nil when it makes none.
// Main starts it, so the interface can say so too.
var updates *backgroundCheck

// upperFirst upper-cases the first letter of s, so a fix reads as a sentence.
// A fix that starts with a backtick, as a command does, is left alone.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// groupRun is the run function of a command that only holds subcommands. Cobra
// reaches it only when no subcommand matched: with no arguments it prints the
// group's help, and with any it fails as the root does for an unknown command,
// with the commands the first argument may have meant.
func groupRun(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	// Only the root sets how near a name must be; a group asks it.
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = cmd.Root().SuggestionsMinimumDistance
	}
	return unknownCommand(cmd, args[0], cmd.SuggestionsFor(args[0]))
}

// unknownCommand is the cli.usage error for arg, which is no command of
// cmd's, with the commands it may have meant.
func unknownCommand(cmd *cobra.Command, arg string, suggestions []string) error {
	m := report.Errorf(report.CLIUsage, "Unknown command %q for %q.", arg, cmd.CommandPath())
	for i, s := range suggestions {
		text := ""
		if i == 0 {
			text = "Did you mean this?"
		}
		m.Step(text, s)
	}
	return m
}
