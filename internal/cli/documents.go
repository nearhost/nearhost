package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/desktop"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// documentsIdle is how long a server's SFTP client for documents is kept
// unused before it is closed.
const documentsIdle = 2 * time.Minute

// shower serves the documents agents ask to show: it keeps the table of
// served trees and the pool of file systems they are read through, and is
// the agent API's Shower.
type shower struct {
	table *documents.Table
	pool  *documents.Pool
	eng   *atomic.Pointer[engine.Engine]
	// open opens a URL in the laptop's browser; tests replace it.
	open func(url string) error
	// stop ends the watch on the engine's events, once attach has started
	// it; nil until then.
	stop atomic.Pointer[func()]
}

// newShower returns a shower with no trees, reading servers' files through
// eng once it is attached.
func newShower(eng *atomic.Pointer[engine.Engine]) *shower {
	s := &shower{table: documents.NewTable(time.Now), eng: eng, open: desktop.OpenURL}
	s.pool = documents.NewPool(s.dial, documentsIdle)
	return s
}

// dial opens server's files over its session. A server that isn't connected
// fails with documents.ErrNotConnected, which the served pages answer with
// the page that says so.
func (s *shower) dial(ctx context.Context, server string) (documents.FS, io.Closer, error) {
	eng := s.eng.Load()
	if eng == nil {
		return nil, nil, fmt.Errorf("%w: Nearhost is still starting", documents.ErrNotConnected)
	}
	f, err := eng.OpenFiles(ctx, server)
	if errors.Is(err, engine.ErrNotConnected) {
		return nil, nil, fmt.Errorf("%w: %w", documents.ErrNotConnected, err)
	}
	if err != nil {
		return nil, nil, err
	}
	return documents.SFTPFS(f.Client), f, nil
}

// badDocument is a document that can't be shown; it reads as why, and
// wraps agentapi.ErrBadDocument.
type badDocument string

func (e badDocument) Error() string { return string(e) }

// Unwrap is agentapi.ErrBadDocument.
func (e badDocument) Unwrap() error { return agentapi.ErrBadDocument }

// noPage is an error of the engine's PageURL, marked as agentapi.ErrNoPage;
// it reads as the engine's error.
type noPage struct{ error }

// Unwrap is the engine's error and agentapi.ErrNoPage.
func (e noPage) Unwrap() []error { return []error{e.error, agentapi.ErrNoPage} }

// notConnected marks err as agentapi.ErrNotConnected when it says the server
// isn't connected, as the documents package or the engine says it.
func notConnected(err error) error {
	if err != nil && !errors.Is(err, agentapi.ErrNotConnected) &&
		(errors.Is(err, documents.ErrNotConnected) || errors.Is(err, engine.ErrNotConnected)) {
		return fmt.Errorf("%w: %w", agentapi.ErrNotConnected, err)
	}
	return err
}

// Resolve finds the document at p on origin's machine, with its symlinks
// resolved, and the tree it is shown from: root when the root rules accept
// it, and otherwise the document's folder.
func (s *shower) Resolve(ctx context.Context, origin, p, root string) (agentapi.Resolved, error) {
	fsys, err := s.pool.Open(ctx, origin)
	if err != nil {
		return agentapi.Resolved{}, notConnected(err)
	}
	doc, err := fsys.RealPath(ctx, p)
	if err != nil {
		return agentapi.Resolved{}, notConnected(err)
	}
	info, err := fsys.Stat(ctx, doc)
	if err != nil {
		return agentapi.Resolved{}, notConnected(err)
	}
	if !info.Mode().IsRegular() {
		return agentapi.Resolved{}, badDocument(p + " isn't a file")
	}
	if !documents.IsDocument(doc) {
		return agentapi.Resolved{}, badDocument(fmt.Sprintf("%s isn't markdown or HTML: Nearhost shows .md, .markdown, .html and .htm files", p))
	}
	home, err := fsys.Home(ctx)
	if err != nil {
		return agentapi.Resolved{}, notConnected(err)
	}
	if real, err := fsys.RealPath(ctx, home); err == nil {
		home = real
	}
	// A root that can't be resolved is set aside for the document's folder,
	// unless the server went away meanwhile.
	sent, err := fsys.RealPath(ctx, root)
	if err != nil {
		if err := notConnected(err); errors.Is(err, agentapi.ErrNotConnected) {
			return agentapi.Resolved{}, err
		}
		sent = ""
	}
	hasGit := func(dir string) bool {
		_, err := fsys.Stat(ctx, path.Join(dir, ".git"))
		return err == nil
	}
	chosen, ok := documents.ChooseRoot(sent, doc, home, hasGit)
	if !ok {
		return agentapi.Resolved{}, badDocument("Nearhost won't serve your whole home folder; save the document in a project or a folder of its own")
	}
	rel, ok := documents.Below(chosen, doc)
	if !ok {
		return agentapi.Resolved{}, badDocument(p + " is a dotfile, which Nearhost doesn't serve")
	}
	// The served pages refuse a backslash in a name, so its URL would only 404.
	if strings.Contains(rel, "\\") {
		return agentapi.Resolved{}, badDocument(p + ": Nearhost can't serve a file whose name contains a backslash")
	}
	return agentapi.Resolved{Root: chosen, Doc: rel}, nil
}

// Served reports whether the tree at root on origin is served, and the
// requester it was approved for.
func (s *shower) Served(origin, root string) (string, bool) {
	tr, ok := s.table.Find(origin, root)
	return tr.Requester, ok
}

// Show serves the tree at root on origin, when it isn't already, opens doc
// in the laptop's browser and returns its URL. A browser that fails to open
// fails nothing, as the agent still gives the user the URL; the log says so.
func (s *shower) Show(ctx context.Context, origin, root, doc, requester string) (string, error) {
	eng := s.eng.Load()
	if eng == nil {
		return "", noPage{errors.New("Nearhost is still starting; try again")}
	}
	base, err := eng.PageURL()
	if err != nil {
		return "", noPage{err}
	}
	tr := s.table.Add(origin, root, requester, doc)
	url := documents.URLFor(base, tr, doc)
	if err := s.open(url); err != nil {
		eng.Events().Publish(engine.Event{Message: fmt.Sprintf("Could not open %s in the browser: %v", url, err)})
	}
	return url, nil
}

// openNewest opens the newest document of the tree id in the laptop's
// browser. A tree no longer served, or one with no documents, has nothing
// to open.
func (s *shower) openNewest(id string) error {
	tr, ok := s.table.Get(id)
	if !ok || len(tr.Docs) == 0 {
		return errors.New("that folder isn't served any more")
	}
	eng := s.eng.Load()
	if eng == nil {
		return errors.New("Nearhost is still starting; try again")
	}
	base, err := eng.PageURL()
	if err != nil {
		return err
	}
	url := documents.URLFor(base, tr, tr.Docs[0].Path)
	if err := s.open(url); err != nil {
		return fmt.Errorf("could not open %s in the browser: %w", url, err)
	}
	return nil
}

// homeTrees are the served trees as Nearhost's page lists them, leaving out
// any with no documents. Their links are relative to the page, so they work
// on whichever address it is served at.
func (s *shower) homeTrees() []proxy.HomeTree {
	var out []proxy.HomeTree
	for _, tr := range s.table.List("") {
		if len(tr.Docs) == 0 {
			continue
		}
		ht := proxy.HomeTree{Server: tr.Server, Root: tr.Root}
		for _, d := range tr.Docs {
			ht.Docs = append(ht.Docs, proxy.HomeDoc{Path: d.Path, URL: documents.URLFor("", tr, d.Path)})
		}
		out = append(out, ht)
	}
	return out
}

// serve has Nearhost's page serve the trees and list them.
func (s *shower) serve() {
	proxy.SetDocuments(documents.NewHandler(s.table, s.pool.Open), s.homeTrees)
}

// watch drops a server's client from the pool whenever the engine says
// something of the server while it isn't connected, so a client whose
// session went away is not kept until it idles out. It runs until close.
func (s *shower) watch(eng *engine.Engine) {
	events, unsubscribe := eng.Events().Subscribe(64)
	if !s.stop.CompareAndSwap(nil, &unsubscribe) {
		unsubscribe() // watching already
		return
	}
	go func() {
		for ev := range events {
			if ev.Server == "" || ev.Project != "" {
				continue
			}
			if st, _ := eng.ServerStatus(ev.Server); st != transport.StatusConnected {
				s.pool.Drop(ev.Server)
			}
		}
	}()
}

// close stops serving the trees and closes the pool.
func (s *shower) close() {
	if stop := s.stop.Load(); stop != nil {
		(*stop)()
	}
	proxy.SetDocuments(nil, nil)
	s.pool.Close()
}

// Compile-time check that the shower is the agent API's Shower.
var _ agentapi.Shower = (*shower)(nil)
