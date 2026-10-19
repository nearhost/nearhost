package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// reviewProject is the engine's Options.Review: it reads a project's
// .nearhostrc through files and compares it with what was last approved
// (p.Nearhostrc). A change that only moves ports, relabels routes or adds
// .test hosts is saved with save and handed back as Updated; one that asks
// for a host that needs approval is handed back as Pending and saved by no
// one until the user approves it. A file that cannot be read or merged, or
// one that is gone after being approved, is a Problem: the project keeps
// serving what was approved, which is still in the config.
func reviewProject(save func(config.Project) error) func(ctx context.Context, p config.Project, files discovery.FileReader) engine.Review {
	return func(_ context.Context, p config.Project, files discovery.FileReader) engine.Review {
		f, found, err := nearhostrc.Read(files)
		switch {
		case err != nil:
			return engine.Review{Problem: err.Error()}
		case !found && p.Nearhostrc == nil:
			return engine.Review{}
		case !found:
			return engine.Review{Problem: nearhostrc.FileName + " is gone; serving what was last approved"}
		case p.Nearhostrc != nil && f.SHA256 == p.Nearhostrc.SHA256:
			return engine.Review{}
		}
		// The routes the file doesn't list are kept from the config: there
		// is no discovery run at start to say what else the project has.
		next, err := nearhostrc.Merge(p, f, nil)
		if err != nil {
			return engine.Review{Problem: err.Error()}
		}
		if items := nearhostrc.NeedsApproval(p, next); len(items) > 0 {
			return engine.Review{Pending: &engine.Change{SHA256: f.SHA256, Project: next, Items: items}}
		}
		if err := save(next); err != nil {
			return engine.Review{Problem: fmt.Sprintf("%s changed only ports or labels, but saving the config failed: %v", nearhostrc.FileName, err)}
		}
		return engine.Review{Updated: &next}
	}
}

// saveMu serialises the saves of saveTo, so two projects whose .nearhostrc
// changed at once do not each save a config without the other's change.
var saveMu sync.Mutex

// saveTo returns a save for reviewProject that writes a project into the
// config file at path: it loads the file, replaces the project of the same
// name, validates and saves, as EditConfig does for the interface. A project
// no longer in the file is not added back.
func saveTo(path string) func(config.Project) error {
	return func(p config.Project) error {
		saveMu.Lock()
		defer saveMu.Unlock()
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		i := -1
		for j := range cfg.Projects {
			if cfg.Projects[j].Name == p.Name {
				i = j
				break
			}
		}
		if i < 0 {
			return fmt.Errorf("project %q is no longer in %s", p.Name, path)
		}
		cfg.Projects[i] = p
		if err := cfg.Validate(); err != nil {
			return err
		}
		return config.Save(path, cfg)
	}
}
