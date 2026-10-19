package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/transport"
)

// reviewTimeout bounds opening SFTP to a server to read a project's
// .nearhostrc, and the review that reads through it.
const reviewTimeout = 20 * time.Second

// PendingPrefix begins the log line a start or Recheck publishes for a
// project whose .nearhostrc asks for changes that need approval; the name
// of each, a host or an option such as docker_clients, follows, separated
// by commas.
const PendingPrefix = nearhostrc.FileName + " asks for changes that need approval: "

// Change is a project's .nearhostrc asking for what was not approved.
type Change struct {
	SHA256  string
	Project config.Project    // the project as it would be with the change
	Items   []nearhostrc.Item // what needs approval
}

// Review is what a re-read of a project's .nearhostrc found.
type Review struct {
	Updated *config.Project // changed without needing approval, and already saved; start with it
	Pending *Change         // waits for approval; the approved routes keep serving
	Problem string          // the file could not be read or parsed; the approved routes keep serving
}

// readRc runs Options.Review for p, reading the project's directory on this
// machine when sess is nil and over SFTP through sess otherwise. A server
// whose files cannot be reached gives a Problem, so the start goes on with
// what was approved. The caller holds no lock: SFTP and the review can block.
func (e *Engine) readRc(ctx context.Context, p config.Project, sess *transport.Session) Review {
	// The review gets a copy of its own, so nothing it does to the routes
	// reaches the engine's configuration, which is never changed in place.
	p.Routes = cloneRoutes(p.Routes)
	if sess == nil {
		return e.opts.Review(ctx, p, discovery.DirReader{Root: p.Path})
	}
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	f, err := sess.OpenSFTP(ctx)
	if err != nil {
		return Review{Problem: fmt.Sprintf("cannot read %s: %v", nearhostrc.FileName, err)}
	}
	// A read from a server that stops answering never returns by itself, and
	// would hold the start, and so Stop and Shutdown, until it did. Closing
	// the client when ctx ends, at the timeout or when the start is
	// cancelled, makes the read fail instead. The once keeps that close and
	// the one after the review from running together.
	var once sync.Once
	closeSFTP := func() { once.Do(func() { f.Close() }) }
	stop := context.AfterFunc(ctx, closeSFTP)
	defer func() {
		stop()
		closeSFTP()
	}()
	return e.opts.Review(ctx, p, f.Reader(p.Path))
}

// noteReviewLocked records what a review found on run and says so in the
// project's log. A pending change the user declined is dropped while the file
// keeps that hash. Only a read that worked tells whether the file changed, so
// a Problem keeps the declined hash; everything else forgets it unless it
// matches. An Updated review leaves nothing pending: what it found is saved.
func (e *Engine) noteReviewLocked(run *projectRun, r Review) {
	run.notice, run.pending = r.Problem, nil
	switch {
	case r.Pending != nil && r.Pending.SHA256 == run.declined:
	case r.Pending != nil:
		run.pending, run.declined = r.Pending, ""
		names := make([]string, len(r.Pending.Items))
		for i, it := range r.Pending.Items {
			names[i] = it.Name()
		}
		e.logProject(run, "%s%s", PendingPrefix, strings.Join(names, ", "))
	case r.Problem != "":
		e.logProject(run, "%s", r.Problem)
	default:
		run.declined = ""
	}
}

// adoptLocked makes the routes, approved snapshot and alias of updated those
// of run's project in the engine's own configuration, as the saved file has
// them, so the Apply that follows from that file finds nothing changed and
// does not restart the project. They are all a .nearhostrc changes; the
// server never changes. A new alias becomes the run's here, under mu, before
// anything is served under it: setConfigLocked rebuilds the alias index the
// proxy's logs are filed by and the hosts file names from the new
// configuration, and the start goes on to check clashes and make the hosts
// resolve with the project returned. It returns the project to start with,
// or nil when Apply has already replaced or removed the project, whose run is
// then never started again anyway.
func (e *Engine) adoptLocked(run *projectRun, updated config.Project) *config.Project {
	if run.gone || e.runs[run.name] != run {
		return nil
	}
	next := cloneConfig(e.cfg)
	for i := range next.Projects {
		if next.Projects[i].Name != run.name {
			continue
		}
		q := &next.Projects[i]
		q.Routes = cloneRoutes(updated.Routes)
		q.Nearhostrc = updated.Nearhostrc.Clone()
		q.Alias = updated.Alias
		run.alias = updated.Alias
		e.setConfigLocked(next, nil) // the runs stay as they are
		e.logProject(run, "%s changed nothing that needs approval; applied", nearhostrc.FileName)
		out := *q
		return &out
	}
	return nil
}

// reviewStart reviews p's .nearhostrc for a start and returns the project to
// go on with: the updated one when the change needed no approval, p itself
// otherwise. The caller holds run.op and not mu.
func (e *Engine) reviewStart(ctx context.Context, run *projectRun, p *config.Project, sess *transport.Session) *config.Project {
	if e.opts.Review == nil {
		return p
	}
	r := e.readRc(ctx, *p, sess)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.noteReviewLocked(run, r)
	if r.Updated != nil {
		if q := e.adoptLocked(run, *r.Updated); q != nil {
			return q
		}
	}
	return p
}

// Recheck reads a project's .nearhostrc again without restarting it. A
// Pending or Problem shows in the project's status. An Updated has already
// been saved by Options.Review, and the Apply of the saved file restarts the
// project with it; the engine's configuration is left alone so that Apply
// sees the change. A remote project needs its server connected.
func (e *Engine) Recheck(ctx context.Context, name string) error {
	e.mu.Lock()
	p, ok := e.cfg.Project(name)
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unknown project %q", name)
	}
	run := e.runs[name]
	var sess *transport.Session
	if p.Server != config.LocalServer {
		c := e.servers[p.Server]
		if c == nil || c.sess.Status() != transport.StatusConnected {
			e.mu.Unlock()
			return fmt.Errorf("server %q is not connected", p.Server)
		}
		sess = c.sess
	}
	proj := *p
	e.mu.Unlock()
	if e.opts.Review == nil {
		return nil
	}

	r := e.readRc(ctx, proj, sess)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runs[name] == run { // a run Apply replaced has nothing to show
		e.noteReviewLocked(run, r)
	}
	return nil
}

// DeclineChange forgets a project's pending change with hash sha. A later
// review that finds the same hash is taken as no change, until the file's
// hash changes.
func (e *Engine) DeclineChange(name, sha string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.runs[name]
	if run == nil {
		return
	}
	run.declined = sha
	if run.pending != nil && run.pending.SHA256 == sha {
		run.pending = nil
	}
}
