package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/service"
)

// serviceBackend is the seam between `nearhost service` and the machine.
type serviceBackend struct {
	// manager is the service manager of this OS, nil on one with none.
	manager func() service.Manager
	// exe is the path of this program.
	exe      func() string
	getenv   func(string) string
	lookPath func(string) (string, error)
	goos     string
	// euid is the user the program runs as, -1 on Windows, which has none.
	euid func() int
	// detach reaches the running core and waits for it to stop or start.
	detach detachBackend
	// interactive reports whether there is a terminal to ask at.
	interactive func() bool
	// home is the user's home directory.
	home string
	// mayNotBind reports whether a core run from exe would surely be
	// refused the proxy's ports below 1024, as a Linux binary without
	// cap_net_bind_service is. Nil, or false when it cannot be told, lets
	// the install go on.
	mayNotBind func(exe string) bool
}

// systemService is the production backend.
func systemService() serviceBackend {
	home, _ := os.UserHomeDir()
	b := serviceBackend{
		exe:      systemDetach().exe,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		goos:     runtime.GOOS,
		euid:     os.Geteuid,
		detach:   systemDetach(),
		interactive: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
		home: home,
	}
	b.mayNotBind = linuxMayNotBind
	b.manager = func() service.Manager {
		if home == "" {
			return nil
		}
		return service.New(service.DefaultNames, home, service.ExecRunner)
	}
	return b
}

func newServiceCmd(b serviceBackend) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run Nearhost as a service that starts at login",
		Long: "Install or remove the Nearhost service: the background Nearhost that starts\n" +
			"when you log in (a launchd agent on macOS, a systemd user service on Linux,\n" +
			"a logon task on Windows).",
		RunE: groupRun,
	}
	var yes bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the Nearhost service and start it",
		Long: "Install the service, or bring it up to date, so that Nearhost starts at login\n" +
			"and runs this copy of nearhost with your PATH and Docker settings. When the\n" +
			"settings change while the service's Nearhost runs, it offers to restart it.\n" +
			"Run it as yourself, not as root.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceInstall(cmd, b, yes)
		},
	}
	install.Flags().BoolVarP(&yes, "yes", "y", false, "restart the running service's Nearhost without asking when its settings changed")
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Nearhost service",
		Long: "Remove the service. The Nearhost it started is stopped; one started any\n" +
			"other way is left running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceUninstall(cmd, b)
		},
	}
	cmd.AddCommand(install, uninstall)
	return cmd
}

// managerFailed is the service.manager-failed error of a service manager
// that could not do what, with its own words, err, quoted as they came:
// they can name a command and its output. The errors that say what this
// system lacks are messages of their own and are returned as they are.
func managerFailed(what string, err error) error {
	if _, ok := report.As(err); ok {
		return err
	}
	return report.Errorf(report.ServiceManagerFailed, "The service manager could not %s.", what).
		Quote(strings.Split(strings.TrimSpace(err.Error()), "\n")...).Wrap(err)
}

// serviceManager returns the manager, or reports that there is none.
func (b serviceBackend) serviceManager(cmd *cobra.Command) (service.Manager, error) {
	var m service.Manager
	if b.manager != nil {
		m = b.manager()
	}
	if m == nil {
		return nil, service.ErrUnsupported
	}
	return m, nil
}

// serviceArgs are the arguments of the core the service starts.
var serviceArgs = []string{"daemon", "--started-by", string(control.StartedService)}

// withManager is b.detach with the service looked up as the manager m and
// the definition d, which is what startCore starts through.
func (b serviceBackend) withManager(m service.Manager, d service.Definition) detachBackend {
	det := b.detach
	det.service = func(context.Context) (service.Manager, service.Definition) { return m, d }
	return det
}

// runServiceInstall is `nearhost service install`.
func runServiceInstall(cmd *cobra.Command, b serviceBackend, yes bool) error {
	out := cmd.OutOrStdout()
	if b.euid() == 0 {
		return report.Errorf(report.ServiceAsRoot, "Install the service as yourself, not as root.").
			Because("The service runs as the user who installs it.").
			Step("Run it again without sudo:", "nearhost service install")
	}
	m, err := b.serviceManager(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	exe, note, err := service.ServiceExe(b.goos, b.exe(), b.getenv, b.lookPath)
	if err != nil {
		return err
	}
	// A service whose core cannot bind its ports fails at every login, so
	// none is installed until the binary may.
	if b.mayNotBind != nil && b.mayNotBind(exe) {
		m := report.Errorf(report.ServiceCannotBind, "The service cannot run Nearhost from this copy yet.").Because(setcapWhy)
		m.Fix = []report.Step{setcapStep(exe, true)}
		return m.Then("Then run nearhost service install again.").SuggestDoctor().With("path", exe)
	}
	logPath, err := config.LogPath()
	if err != nil {
		return err
	}
	// The service sends its output to the log, whose folder the manager does
	// not create: systemd fails the unit before it runs when it is missing.
	// Made here, also when the service is up to date, so installing again
	// heals a service whose folder has gone.
	if err := makeLogFolder(logPath); err != nil {
		return err
	}
	env, notes := service.ServiceEnv(b.goos, b.getenv, m.ManagerEnv(ctx), b.home, b.getenv("XDG_RUNTIME_DIR"))
	def := service.Definition{Exe: exe, Env: env, LogPath: logPath, Graphical: m.Graphical(ctx)}
	if note != nil {
		notes = append([]*report.Message{note}, notes...)
	}
	sink := sinkOf(cmd)
	for _, n := range notes {
		sink.Report(n)
	}
	changed, err := m.Install(ctx, def)
	if err != nil {
		return managerFailed("install the Nearhost service", err)
	}
	if !changed {
		fmt.Fprintln(out, "The Nearhost service is installed and up to date.")
		return nil
	}
	installed := fmt.Sprintf("Installed the Nearhost service. It starts at login and runs %s.", printable(exe))
	det := b.withManager(m, def)
	c, info, running := detachInfo(ctx, det)
	switch {
	case !running:
		// Said before the start, which says on standard error how it fails:
		// the service is installed either way.
		fmt.Fprintln(out, installed)
		_, info, spawned, err := startCore(cmd, det, serviceArgs, control.StartedService, true)
		if err != nil {
			return err
		}
		// A core that answers as the service's is its own, whoever started it:
		// a manager may start the job on install.
		if !spawned && info.StartedBy != control.StartedService {
			// Another nearhost started a core while this one did.
			fmt.Fprintln(out, strings.TrimPrefix(handedOver(info), " "))
			return nil
		}
		fmt.Fprintf(out, "Nearhost is running now (pid %d).\n", info.PID)
	case info.StartedBy == control.StartedService:
		fmt.Fprintln(out, installed)
		restart := yes
		if !restart && b.interactive() {
			restart = askYesNo(cmd.InOrStdin(), out, "The service's settings changed. Restart Nearhost now to apply them? Open passthroughs close. [Y/n] ")
		}
		if !restart {
			fmt.Fprintln(out, "The new settings apply from Nearhost's next start.")
			return nil
		}
		if err := stopCore(ctx, det, c); err != nil {
			return err
		}
		if _, _, _, err := startCore(cmd, det, serviceArgs, control.StartedService, true); err != nil {
			return err
		}
		fmt.Fprintln(out, "Restarted Nearhost with the new settings.")
	default:
		fmt.Fprintln(out, installed+handedOver(info))
	}
	return nil
}

// linuxMayNotBind is the production mayNotBind. On Linux, the service's core
// serves on ports 443 and 80, which a binary may bind with
// cap_net_bind_service, or any binary may when the system's first
// unprivileged port is 80 or lower. Elsewhere file capabilities cannot be
// told, so it is false.
func linuxMayNotBind(exe string) bool {
	if first, ok := readUnprivilegedPortStart(); ok && first <= 80 {
		return false
	}
	has, known := fileNetBindCap(exe)
	return known && !has
}

// makeLogFolder creates the folder of the log at logPath, which only its
// owner may read, as the core makes it.
func makeLogFolder(logPath string) error {
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the log folder %s: %w", dir, err)
	}
	return nil
}

// handedOver is what install adds when a core that is not the service's runs.
func handedOver(info control.Info) string {
	return fmt.Sprintf(" Nearhost is already running (started %s), so the service takes over from your next login. To hand over now, run nearhost stop, then nearhost -d.", startedHow(info.StartedBy))
}

// stopCore asks the core c to stop and waits until it has gone.
func stopCore(ctx context.Context, b detachBackend, c *control.Client) error {
	// A core that stops between the dial and this call has done what was
	// asked.
	if err := c.Stop(ctx); err != nil && !errors.Is(err, control.ErrNotRunning) {
		return err
	}
	if b.gone != nil {
		return b.gone(ctx)
	}
	return nil
}

// runServiceUninstall is `nearhost service uninstall`.
func runServiceUninstall(cmd *cobra.Command, b serviceBackend) error {
	out := cmd.OutOrStdout()
	m, err := b.serviceManager(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	_, ok, err := m.Installed(ctx)
	switch {
	case errors.Is(err, service.ErrUnreadable):
		// A definition edited by hand is still the service's, and the
		// managers remove one without reading it.
		sinkOf(cmd).Report(report.Warnf(report.ServiceUnreadable, "The Nearhost service cannot be read, so it is removed as it is.").
			Quote(strings.Split(strings.TrimSpace(err.Error()), "\n")...).Wrap(err))
	case err != nil:
		return managerFailed("read the Nearhost service", err)
	case !ok:
		fmt.Fprintln(out, "No Nearhost service is installed.")
		return nil
	}
	stopped := false
	// Only the service's own core goes with it: one started any other way
	// is not the service's to stop.
	if c, info, running := detachInfo(ctx, b.detach); running && info.StartedBy == control.StartedService {
		if err := stopCore(ctx, b.detach, c); err != nil {
			return err
		}
		stopped = true
	}
	if err := m.Uninstall(ctx); err != nil {
		if stopped {
			// Said before the error, as the user may think it still runs.
			fmt.Fprintln(out, "Stopped Nearhost, which the service ran.")
		}
		return managerFailed("remove the Nearhost service", err)
	}
	msg := "Removed the Nearhost service."
	if stopped {
		msg += " Stopped Nearhost, which it ran."
	}
	fmt.Fprintln(out, msg)
	return nil
}

// askYesNo prints prompt and reads the answer: yes or just enter is true, no
// is false, and anything else is asked again. Input that ends unanswered is
// no.
func askYesNo(in io.Reader, out io.Writer, prompt string) bool {
	r := bufio.NewReader(in)
	for {
		fmt.Fprint(out, prompt)
		line, err := r.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			if err != nil && strings.TrimSpace(line) == "" {
				fmt.Fprintln(out)
				return false
			}
			return true
		case "n", "no":
			return false
		}
		if err != nil {
			fmt.Fprintln(out)
			return false
		}
	}
}
