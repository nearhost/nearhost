package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/tui"
)

// What the interface asks of nearhost to change what is configured.

func (u *uiEngine) EditConfig(fn func(cfg *config.Config) error) error {
	u.editMu.Lock()
	defer u.editMu.Unlock()
	cfg, err := u.current()
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return config.Save(u.path, cfg)
}

func (u *uiEngine) ReconnectServer(_ context.Context, name string) error {
	return u.Engine.ReconnectServer(name)
}

func (u *uiEngine) DisconnectServer(_ context.Context, name string) error {
	return u.Engine.DisconnectServer(name)
}

func (u *uiEngine) ConnectServer(_ context.Context, name string) error {
	return u.Engine.ConnectServer(name)
}

// Advice works out the advice for a configured project, on a connection of
// its own to the project's server.
func (u *uiEngine) Advice(ctx context.Context, project string) (tui.ProjectAdvice, error) {
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
	uf := &uiFiles{u: u, srv: srv, files: files}
	advice, err := uf.advise(ctx, cfg, p.Path, p.Routes, p.Alias, p.Name)
	return advice, errors.Join(err, files.close())
}

// Trust runs what `nearhost trust` does, as root, asking for the password
// in the interface when sudo needs it. Its output goes to the terminal only
// when it runs there.
func (u *uiEngine) Trust(ctx context.Context) error {
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
	return err
}
