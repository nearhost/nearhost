package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// reviewProject is the engine's Options.Review: it reads a project's
// .nearhostrc through files and compares it with what was last approved
// (p.Nearhostrc). A change that only moves ports, relabels routes, adds .test
// hosts or moves the main name to another .test name is saved with save and
// handed back as Updated; one that asks for a host or main name that needs
// approval is handed back as Pending and saved by no one until the user
// approves it. A file that cannot be read or merged, one
// that is gone after being approved, or a change save refuses because
// another enabled project serves one of its hosts, is a Problem: the project keeps
// serving what was approved, which is still in the config.
func reviewProject(save func(config.Project) error) func(ctx context.Context, p config.Project, files discovery.FileReader) engine.Review {
	return func(_ context.Context, p config.Project, files discovery.FileReader) engine.Review {
		f, found, err := nearhostrc.Read(files)
		c := classifyRc(p, f, found, err)
		switch c.kind {
		case rcProblem:
			return engine.Review{Problem: c.problem}
		case rcNothing:
			return engine.Review{}
		case rcPending:
			return engine.Review{Pending: &engine.Change{SHA256: f.SHA256, Project: c.next, Items: c.items}}
		}
		if err := save(c.next); err != nil {
			var clash *hostClashError
			if errors.As(err, &clash) {
				return engine.Review{Problem: fmt.Sprintf("%s: %v; serving what was last approved", nearhostrc.FileName, err)}
			}
			return engine.Review{Problem: fmt.Sprintf("%s changed nothing that needs approval, but saving the config failed: %v", nearhostrc.FileName, err)}
		}
		return engine.Review{Updated: &c.next}
	}
}

// rcKind is what a project's .nearhostrc, as read, asks of it.
type rcKind int

const (
	// rcNothing: there is no file and none was approved, or the file is
	// the approved one.
	rcNothing rcKind = iota
	// rcUpdated: the file changed only what needs no approval.
	rcUpdated
	// rcPending: the file asks for a host or main name that needs the
	// user's approval.
	rcPending
	// rcProblem: the file cannot be read or merged, or is gone after being
	// approved.
	rcProblem
)

// rcClass is how a read .nearhostrc stands against a project: its kind, the
// project it merges to and what of that needs approval, or the problem.
type rcClass struct {
	kind    rcKind
	next    config.Project
	items   []nearhostrc.Item
	problem string
}

// classifyRc says how the .nearhostrc that nearhostrc.Read returned as f,
// found and err stands against p and its approved snapshot. It is the
// review's judgement, kept in one place so that export says a change was
// waiting for approval exactly when the review would have held it. The
// routes the file doesn't list are kept from p: there is no discovery run at
// start to say what else the project has.
func classifyRc(p config.Project, f *nearhostrc.File, found bool, err error) rcClass {
	switch {
	case err != nil:
		return rcClass{kind: rcProblem, problem: err.Error()}
	case !found && p.Nearhostrc == nil:
		return rcClass{kind: rcNothing}
	case !found:
		return rcClass{kind: rcProblem, problem: nearhostrc.FileName + " is gone; serving what was last approved"}
	case p.Nearhostrc != nil && f.SHA256 == p.Nearhostrc.SHA256:
		return rcClass{kind: rcNothing}
	}
	next, err := nearhostrc.Merge(p, f, nil)
	if err != nil {
		return rcClass{kind: rcProblem, problem: err.Error()}
	}
	if items := nearhostrc.NeedsApproval(p, next); len(items) > 0 {
		return rcClass{kind: rcPending, next: next, items: items}
	}
	return rcClass{kind: rcUpdated, next: next}
}

// saveMu serialises the saves of saveTo, so two projects whose .nearhostrc
// changed at once do not each save a config without the other's change.
var saveMu sync.Mutex

// saveTo returns a save for reviewProject that writes a project's routes
// into the config file at path: it loads the file, sets the routes and the
// approved snapshot of the project of the same name, validates and saves, as
// EditConfig does for the interface. Only those fields, and the alias when
// the file moved it, are taken from p: p is the engine's copy, which `up`
// loaded at launch, and anything else changed in the file since (enabled,
// path, an alias set by hand) must not be undone. A project no longer in the
// file is not added back.
func saveTo(path string) func(config.Project) error {
	return func(p config.Project) error {
		saveMu.Lock()
		defer saveMu.Unlock()
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		if err := takeServedRc(cfg, p, path); err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		return config.Save(path, cfg)
	}
}

// takeRc sets the routes and the approved snapshot of the project in cfg
// called as p is to p's, and leaves the rest of it as cfg has it. The alias is
// taken too when p's snapshot has one that cfg's snapshot does not, which is
// when Merge moved it to the file's; otherwise it stays as cfg has it, so an
// alias set by hand is kept. A project no longer in cfg, the file at path, is
// not added back.
func takeRc(cfg *config.Config, p config.Project, path string) error {
	for i := range cfg.Projects {
		q := &cfg.Projects[i]
		if q.Name != p.Name {
			continue
		}
		if p.Nearhostrc != nil && p.Nearhostrc.Alias != "" && (q.Nearhostrc == nil || q.Nearhostrc.Alias != p.Nearhostrc.Alias) {
			q.Alias = p.Alias
		}
		q.Routes = p.Routes
		q.Nearhostrc = p.Nearhostrc
		return nil
	}
	return fmt.Errorf("project %q is no longer in %s", p.Name, path)
}

// takeServedRc is takeRc for a change saved without the user's approval. A
// .test host or main name needs no approval, but it can be another enabled
// project's alias or one of its hosts, and two projects cannot serve the same
// host, so the change is refused and the approved routes keep serving.
func takeServedRc(cfg *config.Config, p config.Project, path string) error {
	if err := takeRc(cfg, p, path); err != nil {
		return err
	}
	q, _ := cfg.Project(p.Name)
	if !q.Enabled {
		return nil
	}
	if host, other, clash := cfg.HostClash(*q); clash {
		return &hostClashError{host: host, other: other}
	}
	return nil
}

// hostClashError says a host is already served by another project, so the
// review can say so in its own words.
type hostClashError struct{ host, other string }

func (e *hostClashError) Error() string {
	return fmt.Sprintf("host %s is already served by project %q", e.host, e.other)
}
