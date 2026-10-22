package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
}

// systemUpdate is the production backend. Only NewRoot wires it in.
func systemUpdate() updateBackend {
	return updateBackend{
		latest: update.Checker{HTTP: updateHTTP()}.Latest,
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
			c := exec.CommandContext(ctx, m.Args[0], m.Args[1:]...)
			c.Env = append(os.Environ(), m.Env...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
			return c.Run()
		},
	}
}

func newUpdateCmd(b updateBackend) *cobra.Command {
	var check, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update nearhost to the latest release",
		Long: "Update nearhost to the latest release, the way this copy was installed:\n" +
			"with Homebrew, npm, pnpm, bun, go install or the install script.\n\n" +
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
			fmt.Fprintf(out, "This is a development build of nearhost (%s). The latest release is %s.\n", Version, latest)
		}
		return nil
	}
	exe := b.exe()
	m := update.Detect(exe, b.getenv)
	if check {
		return fmt.Errorf("%s is available (this is %s, installed with %s). Run `nearhost update` to update", latest, current, m.Kind)
	}
	fmt.Fprintf(out, "nearhost %s → %s is available. This copy was installed with %s.\n", current, latest, m.Kind)

	switch {
	case m.Kind == update.Ephemeral:
		fmt.Fprintln(out, "npx fetched this copy for one run. Run `npx @nearhost/cli@latest` to use the latest.")
		return nil
	case m.Kind == update.Script && !b.writable(m.Dir):
		fmt.Fprintf(out, "You cannot write to %s, so run the installer as root:\n\n  curl -fsSL https://nearhost.dev/install.sh | sudo NEARHOST_INSTALL_DIR=%s sh\n", m.Dir, m.Dir)
		return nil
	}

	if !yes {
		if !b.interactive() {
			fmt.Fprintf(out, "Run `%s`, or `nearhost update --yes` to have nearhost run it.\n", m)
			return nil
		}
		fmt.Fprintf(out, "Run `%s`? [Y/n] ", m)
		answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
		default:
			fmt.Fprintln(out, "Not updated.")
			return nil
		}
	}
	if err := b.run(ctx, m, out, cmd.ErrOrStderr()); err != nil {
		return fmt.Errorf("`%s` failed: %w", m, err)
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

// updateHTTP is the client for update checks, short enough that a slow
// network never holds anything up for long.
func updateHTTP() *http.Client { return &http.Client{Timeout: 3 * time.Second} }

// shouldCheckForUpdate reports whether this run of nearhost checks for a new
// release in the background: not for the commands about versions and help,
// not as root, which is sudo running the hosts helper, not when stderr is
// not a terminal to say so on, not for a development build, and not when
// NEARHOST_NO_UPDATE_CHECK is set.
func shouldCheckForUpdate(args []string, getenv func(string) string, version string, euid int, tty bool) bool {
	if getenv("NEARHOST_NO_UPDATE_CHECK") != "" || euid == 0 || !tty {
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
	return update.Checker{HTTP: updateHTTP(), CachePath: path}.Cached(ctx)
}
