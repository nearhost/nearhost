package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/tui"
)

// uiBackend is the seam between bare `nearhost` and the machine.
type uiBackend struct {
	up upBackend
	// interactive reports whether standard input and output are terminals.
	interactive func() bool
	// run runs the interface until it quits. s must be attached to the
	// program that runs m.
	run func(m *tui.Model, s *tui.Suspender) error
	// applier is the privileged hosts write, given the context of the start
	// that needs it.
	applier func(ctx context.Context) hosts.Applier
	// readHosts returns the content of the hosts file.
	readHosts func() (string, error)
	// open opens a URL in the browser.
	open func(url string) error
	// workDir is the directory nearhost was started in.
	workDir func() (string, error)
}

// systemUI is the production backend. Only NewRoot wires it in.
func systemUI() uiBackend {
	return uiBackend{
		up: systemUp(),
		interactive: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
		run: func(m *tui.Model, s *tui.Suspender) error {
			p := tea.NewProgram(m)
			s.Attach(p)
			_, err := p.Run()
			return err
		},
		applier: func(ctx context.Context) hosts.Applier {
			exe, err := os.Executable()
			if err != nil {
				return func([]string) error { return err }
			}
			// The applier ends sudo when ctx does, so a project stopped
			// during the password prompt stops waiting for it.
			return hosts.SudoApplierContext(ctx, exe, hosts.DefaultPath)
		},
		readHosts: func() (string, error) {
			data, err := os.ReadFile(hosts.DefaultPath)
			return string(data), err
		},
		open:    openBrowser,
		workDir: os.Getwd,
	}
}

// openBrowser opens url with the system's opener.
func openBrowser(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Run()
}

// bindUI makes bare `nearhost` open the interface at a terminal, with the
// serving flags of `up`, and print help otherwise.
func bindUI(root *cobra.Command, b uiBackend) {
	var flags serveFlags
	flags.bind(root)
	root.Args = cobra.NoArgs
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if b.interactive == nil || !b.interactive() {
			return cmd.Help()
		}
		return runUI(cmd, b, flags)
	}
}

func runUI(cmd *cobra.Command, b uiBackend, flags serveFlags) error {
	if err := flags.validate(); err != nil {
		return err
	}
	cfg, cfgPath, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	dir, err := b.workDir()
	if err != nil {
		return err
	}
	var suspender tui.Suspender
	opts, err := engineOptions(b.up.ssh, flags, hostsThrough(suspender.Run, hosts.DefaultPath, b.applier))
	if err != nil {
		return err
	}
	eng := engine.New(cfg, opts)
	// The interface shuts the engine down when it quits; this covers a
	// program that ends any other way.
	defer eng.Shutdown(context.Background())
	back := &uiEngine{Engine: eng, b: b, path: cfgPath, flags: flags, storage: opts.StorageRoot}
	return b.run(tui.New(back, dir), &suspender)
}

// hostsThrough is an EnsureHosts for the interface: when the hosts block has
// to change, it suspends the interface, says why the password is needed and
// runs the privileged write in the plain terminal.
func hostsThrough(suspend func(context.Context, func(io.Reader, io.Writer) error) error, path string, applier func(context.Context) hosts.Applier) func(context.Context, []string) error {
	return func(ctx context.Context, aliases []string) error {
		_, err := hosts.Ensure(path, aliases, func(want []string) error {
			return suspend(ctx, func(_ io.Reader, out io.Writer) error {
				fmt.Fprintf(out, "Nearhost needs your password to add %s to %s.\n", strings.Join(want, ", "), path)
				return applier(ctx)(want)
			})
		})
		return err
	}
}

// uiEngine is the interface's backend: the engine, and what the interface
// asks of the machine.
type uiEngine struct {
	*engine.Engine
	b       uiBackend
	path    string
	flags   serveFlags
	storage string
}

func (u *uiEngine) ConfigPath() string { return u.path }

func (u *uiEngine) Ports() (int, int) { return u.flags.httpsPort, u.flags.httpPort }

func (u *uiEngine) CATrusted() bool { return untrustedCA(u.b.up.trust, u.storage) == "" }

func (u *uiEngine) HostsState() string {
	if u.flags.noHosts {
		return "not managed (--no-hosts)"
	}
	c := checkHosts(doctorBackend{readHosts: u.b.readHosts}, u.Config())
	return c.detail
}

func (u *uiEngine) Open(url string) error { return u.b.open(url) }

// AddCommand runs the add questions for dir in the plain terminal, then
// waits for enter so the outcome can be read before the interface returns.
func (u *uiEngine) AddCommand(dir string) tea.ExecCommand {
	return tui.Command(func(in io.Reader, out io.Writer) error {
		root := newRootCmd()
		root.AddCommand(newAddCmd(systemAdd()))
		root.SetArgs([]string{"--config", u.path, "add", dir})
		root.SetIn(in)
		root.SetOut(out)
		root.SetErr(out)
		err := root.Execute()
		if err != nil {
			fmt.Fprintln(out, "nearhost:", err)
		}
		fmt.Fprint(out, "\nPress enter to return to Nearhost.")
		waitForEnter(in)
		return err
	})
}

// waitForEnter reads in up to the end of a line or of the input. It reads a
// byte at a time, so nothing typed after the line is taken from the
// interface.
func waitForEnter(in io.Reader) {
	b := make([]byte, 1)
	for {
		if n, err := in.Read(b); err != nil || (n == 1 && b[0] == '\n') {
			return
		}
	}
}

// uiEngine must satisfy the interface's backend.
var _ tui.Backend = (*uiEngine)(nil)
