package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/nearhostrc"
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

// saveRc is the save of the interface engine's Options.Review: it writes a
// project's routes and approved snapshot through EditConfig, so it never
// saves over an edit the interface is making, and keeps every other field
// as the file has it now. Like saveTo, it refuses a host another enabled
// project serves.
func (u *uiEngine) saveRc(p config.Project) error {
	return u.EditConfig(func(cfg *config.Config) error { return takeServedRc(cfg, p, u.path) })
}

// AcceptChange saves the routes and snapshot of the change with hash sha
// that the project's .nearhostrc waits on. It must be the change pending
// now: the user approved what the review showed, and a newer file may ask
// for more. The config poll then applies the save, which restarts the
// project with it. An agent waiting on the change is told once it is saved.
func (u *uiEngine) AcceptChange(project, sha string) error {
	change := u.Status(project).Pending
	if change == nil || change.SHA256 != sha {
		return fmt.Errorf("the %s changed again; review it again", nearhostrc.FileName)
	}
	err := u.EditConfig(func(cfg *config.Config) error {
		if err := takeRc(cfg, change.Project, u.path); err != nil {
			return err
		}
		p, _ := cfg.Project(project)
		if host, other, clash := cfg.HostClash(*p); clash {
			return fmt.Errorf("host %s is already served by project %q", host, other)
		}
		return nil
	})
	if err == nil {
		u.agents.changeAnswered(project, sha, true)
	}
	return err
}

// DeclineChange is the engine's, and tells an agent waiting on the change.
func (u *uiEngine) DeclineChange(project, sha string) {
	u.Engine.DeclineChange(project, sha)
	u.agents.changeAnswered(project, sha, false)
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
