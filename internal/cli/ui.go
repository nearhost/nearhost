package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

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
	// sshConfig returns the user's OpenSSH client config.
	sshConfig func() ([]byte, error)
	// updateNotice delivers a line saying a new release is out, if one is;
	// nil means no check.
	updateNotice func() <-chan string
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
			p := tea.NewProgram(m)
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
			return hosts.SudoApplierWith(ctx, exe, hosts.DefaultPath, terminalPrompt, !prompt)
		},
		root: systemRoot(),
		readHosts: func() (string, error) {
			data, err := os.ReadFile(hosts.DefaultPath)
			return string(data), err
		},
		lookup:  lookup,
		open:    openBrowser,
		workDir: os.Getwd,
		presets: systemPresets(),
		sshConfig: func() ([]byte, error) {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			return os.ReadFile(filepath.Join(home, ".ssh", "config"))
		},
		updateNotice: func() <-chan string { return updates.wait() },
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
	flags.bindCapture(root)
	root.Args = rootArgs
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if b.interactive == nil || !b.interactive() {
			return cmd.Help()
		}
		return runUI(cmd, b, flags)
	}
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

func runUI(cmd *cobra.Command, b uiBackend, flags serveFlags) error {
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
	dir, err := b.workDir()
	if err != nil {
		return err
	}
	var suspender tui.Suspender
	root := asRoot{sudo: b.root, ask: suspender.Password, suspend: suspender.Run, width: terminalWidth, turn: make(chan struct{}, 1)}
	// Every sudo the interface runs, the CA's included, which cannot be
	// given a prompt of its own, words its prompt as Nearhost's.
	os.Setenv("SUDO_PROMPT", terminalPrompt)
	opts, err := engineOptions(b.up.ssh, flags, hostsThrough(root, hosts.DefaultPath, b.applier), b.lookup)
	if err != nil {
		return err
	}
	// Each start, and r on a project, re-reads its .nearhostrc. A change
	// that needs no approval is saved as the interface's own edits are, one
	// at a time with them; one that does waits for a to review it.
	back := &uiEngine{b: b, path: cfgPath, flags: flags, storage: opts.StorageRoot, root: root}
	opts.Review = reviewProject(back.saveRc)
	ag, agentNote := serveAgents(b.up.agents, &opts, cfgPath, flags.httpsPort, agentEnv{
		ssh: b.up.ssh, doctor: systemDoctorEnv(b.lookup, flags.noHosts, opts.StorageRoot),
	})
	defer ag.close()
	eng := engine.New(cfg, opts)
	back.Engine, back.agents = eng, ag
	ag.attach(eng)
	// The interface shuts the engine down when it quits; this covers a
	// program that ends any other way.
	defer eng.Shutdown(context.Background())
	var proposals tui.Proposals
	ag.review(&proposals)
	m := tui.New(back, dir)
	if agentNote != "" {
		eng.Events().Publish(engine.Event{Message: agentNote})
	}
	if b.updateNotice != nil {
		go func() {
			for n := range b.updateNotice() {
				eng.Events().Publish(engine.Event{Message: n})
			}
		}()
	}
	return b.run(m, func(p *tea.Program) {
		suspender.Attach(p)
		proposals.Attach(p)
	})
}

// hostsThrough is an EnsureHosts for the interface: when the hosts block has
// to change, it runs the privileged write as root, asking for the password
// in the interface when sudo needs it.
func hostsThrough(root asRoot, path string, applier func(ctx context.Context, prompt bool) hosts.Applier) func(context.Context, []string) error {
	return func(ctx context.Context, aliases []string) error {
		_, err := hosts.Ensure(path, aliases, func(want []string) error {
			// A start that has ended since it asked needs no password.
			if err := ctx.Err(); err != nil {
				return err
			}
			names := strings.Join(want, ", ")
			why := fmt.Sprintf("Nearhost needs your password to add %s to %s.", names, path)
			err := root.run(ctx, why, "Added to "+path+".", func(ctx context.Context, terminal io.Writer) error {
				return applier(ctx, terminal != nil)(want)
			})
			if errors.Is(err, tui.ErrPasswordCancelled) {
				verb := "were"
				if len(want) == 1 {
					verb = "was"
				}
				return fmt.Errorf("you cancelled the password, so %s %s not added to %s", names, verb, path)
			}
			return err
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
	// root runs steps that need root.
	root asRoot
	// agents answers the agents' proposals; nil when none can reach
	// nearhost.
	agents *agents
	// hostsAsked is set by the first HostsState call.
	hostsAsked atomic.Bool
	// acceptSaving, when set, runs inside AcceptChange's save. Tests use it
	// to re-read meanwhile; it is nil otherwise.
	acceptSaving func()
}

func (u *uiEngine) AcceptProposal(id, name string) { u.agents.accept(id, name) }

func (u *uiEngine) DeclineProposal(id, reason string) { u.agents.decline(id, reason) }

func (u *uiEngine) ConfigPath() string { return u.path }

func (u *uiEngine) Ports() (int, int) { return u.flags.httpsPort, u.flags.httpPort }

func (u *uiEngine) CATrusted() bool { return untrustedCA(u.b.up.trust, u.storage) == "" }

func (u *uiEngine) CAExpires() time.Time {
	if u.b.up.trust.rootCert == nil {
		return time.Time{}
	}
	cert, err := u.b.up.trust.rootCert(u.storage)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter
}

func (u *uiEngine) HostsState() string {
	if u.flags.noHosts {
		return "not managed (--no-hosts)"
	}
	// The interface asks once while it is built, before anything is drawn.
	// That call must not wait on DNS; the refresh that follows it does the
	// lookup and replaces this answer.
	if !u.hostsAsked.Swap(true) {
		return "checking"
	}
	// The same names doctor checks, filtered the same way, so the interface
	// and `nearhost doctor` never disagree about what the file should hold.
	c, _ := checkHosts(context.Background(), doctorBackend{env: doctorEnv{readHosts: u.b.readHosts, lookup: u.b.lookup}}, u.Config())
	return c.detail
}

func (u *uiEngine) Open(url string) error { return u.b.open(url) }

// uiEngine must satisfy the interface's backend.
var _ tui.Backend = (*uiEngine)(nil)
