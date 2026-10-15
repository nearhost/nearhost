package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

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

// Advice works out the advice for a configured project, on a connection of
// its own to the project's server.
func (u *uiEngine) Advice(ctx context.Context, project string) ([]tui.Advisory, []string, error) {
	cfg, err := u.current()
	if err != nil {
		return nil, nil, err
	}
	p, ok := cfg.Project(project)
	if !ok {
		return nil, nil, fmt.Errorf("%s is no longer in the config", project)
	}
	srv, ok := cfg.Server(p.Server)
	if !ok {
		return nil, nil, fmt.Errorf("unknown server %q", p.Server)
	}
	files, err := openFiles(ctx, u.b.up.ssh, srv, connectAgain)
	if err != nil {
		return nil, nil, err
	}
	uf := &uiFiles{u: u, srv: srv, files: files}
	advice, notes, err := uf.advise(ctx, cfg, p.Path, p.Routes, p.Alias, p.Name)
	return advice, notes, errors.Join(err, files.close())
}

// Trust runs what `nearhost trust` does, in the plain terminal, so the
// system can ask for a password.
func (u *uiEngine) Trust(ctx context.Context) error {
	if u.suspend == nil {
		return errors.New("the interface is not running")
	}
	return u.suspend(ctx, func(in io.Reader, out io.Writer) error {
		root := newRootCmd()
		root.AddCommand(newTrustCmd(u.b.up.trust))
		root.SetArgs([]string{"trust"})
		root.SetIn(in)
		root.SetOut(out)
		root.SetErr(out)
		return root.ExecuteContext(ctx)
	})
}
