package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/tui"
)

// What the interface asks of nearhost to change what is configured.

func (u *localOps) EditConfig(fn func(cfg *config.Config) error) error {
	return editConfig(u.path, fn)
}

// saveRc is the save of the interface engine's Options.Review: it writes a
// project's routes and approved snapshot through EditConfig, so it never
// saves over an edit the interface is making, and keeps every other field
// as the file has it now. Like saveTo, it refuses a host another enabled
// project serves.
func (u *localOps) saveRc(p config.Project) error {
	return u.EditConfig(func(cfg *config.Config) error { return takeServedRc(cfg, p, u.path) })
}

// Advice works out the advice for a configured project, on a connection of
// its own to the project's server.
func (u *localOps) Advice(ctx context.Context, project string) (tui.ProjectAdvice, error) {
	cfg, err := u.current()
	if err != nil {
		return tui.ProjectAdvice{}, err
	}
	p, ok := cfg.Project(project)
	if !ok {
		return tui.ProjectAdvice{}, fmt.Errorf("%s is no longer in the config", project)
	}
	srv, ok := cfg.Server(p.Server)
	if !ok {
		return tui.ProjectAdvice{}, fmt.Errorf("unknown server %q", p.Server)
	}
	files, err := openFiles(ctx, u.b.up.ssh, srv, connectAgain)
	if err != nil {
		return tui.ProjectAdvice{}, err
	}
	return u.adviceOn(ctx, cfg, *p, srv, files)
}

// adviceOn is Advice for p on srv's files, which it closes. Ending ctx
// closes them too, which ends a read stuck on a server that stopped
// answering, as quitting while a remote project is selected may find.
func (u *localOps) adviceOn(ctx context.Context, cfg *config.Config, p config.Project, srv config.Server, files serverFiles) (tui.ProjectAdvice, error) {
	uf := &uiFiles{u: u, srv: srv, files: files}
	stop := context.AfterFunc(ctx, func() { uf.Close() })
	defer stop()
	advice, err := uf.advise(ctx, cfg, p.Path, p.Routes, p.Alias, p.Name)
	return advice, errors.Join(err, uf.Close())
}

// Trust runs what `nearhost trust` does, as root, asking for the password
// in the interface when the elevator takes one (sudo; on Windows, Windows
// asks). Its output goes to the terminal only
// when it runs there.
func (u *localOps) Trust(ctx context.Context) error {
	if u.root.ask == nil {
		return errors.New("the interface is not running")
	}
	const why = "Nearhost needs your password to trust its local certificate authority, so browsers accept HTTPS from its web server."
	// What trust prints goes nowhere: in the terminal, Nearhost's header
	// says why and what came of it, and sudo's prompt is the only other line.
	err := u.root.run(ctx, why, "Trusted.", func(ctx context.Context, _ io.Writer) error {
		out := io.Discard
		root := newRootCmd()
		root.AddCommand(newTrustCmd(u.b.up.trust))
		root.SetArgs([]string{"trust"})
		root.SetIn(strings.NewReader(""))
		root.SetOut(out)
		root.SetErr(out)
		return root.ExecuteContext(ctx)
	})
	if errors.Is(err, tui.ErrPasswordCancelled) {
		return errors.New("you cancelled the password; the CA is not trusted yet")
	}
	if errors.Is(err, elevate.ErrDeclined) {
		return errors.New("you declined the Windows prompt; the CA is not trusted yet")
	}
	return err
}
