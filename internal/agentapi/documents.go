package agentapi

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Resolved is a document an agent asked to show, as the Shower found it.
type Resolved struct {
	Root string // the tree's root, after the root rules, absolute
	Doc  string // the document's path relative to Root
}

// Shower serves the documents agents ask to show. The documents package
// answers it, adapted by the caller, so agentapi needs neither it nor the
// engine.
type Shower interface {
	// Resolve finds the document at path on origin's machine and the tree
	// it is shown from, applying the root rules to root. Its error wraps
	// ErrNotConnected when the server isn't connected, fs.ErrNotExist when
	// the document isn't there, or ErrBadDocument when it can't be shown.
	Resolve(ctx context.Context, origin, path, root string) (Resolved, error)
	// Served reports whether the tree at root on origin is served already,
	// and the requester it was approved for. It must not block.
	Served(origin, root string) (requester string, ok bool)
	// Show serves the tree at root on origin, when it isn't already,
	// opens doc in the laptop's browser and returns its URL. Its error
	// wraps ErrNoPage when Nearhost's page has no URL to give out.
	Show(ctx context.Context, origin, root, doc, requester string) (url string, err error)
}

// ErrBadDocument is wrapped by Shower.Resolve's error for a document that
// can't be shown: not a regular file, not markdown or HTML, or below a
// dot segment.
var ErrBadDocument = errors.New("not a document nearhost can show")

// ErrNoPage is wrapped by Shower.Show's error when Nearhost's page isn't
// served anywhere a URL can point to.
var ErrNoPage = errors.New("nearhost's page isn't served")

// DocRequest is a document waiting for the user's answer to show it.
type DocRequest struct {
	ID        string
	Origin    string
	Requester string
	Session   string
	Label     string
	Root      string // the tree's root, absolute
	Doc       string // the document, relative to Root
	// CanAlways is set when Always allow may be offered, which it never is
	// to RequesterUnknown.
	CanAlways bool
	// Expires is when the request stops waiting for an answer.
	Expires time.Time
}

// docRecord is a document request and what became of it. done is closed
// when it leaves StatePending.
type docRecord struct {
	DocRequest
	// prompted is set for a request shown to the reviewer; one that opens
	// without asking does not count against maxPending.
	prompted bool
	// opening is set while Shower.Show runs for it. It does not expire
	// meanwhile, and can't be answered again.
	opening bool
	state   State
	reason  string
	url     string
	// token is the Always allow token, until the agent has read it.
	token string
	seq   uint64 // creation order, for lists
	// opened is the order the request opened in, among all document
	// requests, so the latest open one is the one shown last.
	opened uint64
	ended  time.Time
	done   chan struct{}
}

// documenting reports whether this server has documents.
func (s *Server) documenting() bool {
	return s.opts.Shower != nil && s.opts.DocGrants != nil
}

// AnswerDocument answers the document request id with the user's answer,
// as AnswerPassthrough answers a passthrough request. On Yes and Always
// allow it shows the document, blocking while it does, and returns Show's
// error, which also ends the request declined with it as the reason. It
// errors for Yes or Always allow on an id that is unknown or has ended, and
// for Always allow on a request that may not have it. No on an unknown id
// does nothing and returns nil; No on a request that has ended returns the
// same error Yes would. When the grant for Always allow fails to save, the
// error wraps ErrAllowNotSaved. Every error for a request that is no longer
// waiting wraps ErrPassEnded.
func (s *Server) AnswerDocument(ctx context.Context, id string, a Answer) error {
	if !s.documenting() {
		return errors.New("this nearhost has no documents")
	}
	s.mu.Lock()
	s.sweepDocsLocked()
	r := s.docs[id]
	switch {
	case a == AnswerNo && (r == nil || !r.prompted):
		s.mu.Unlock()
		return nil
	case r == nil || !r.prompted:
		s.mu.Unlock()
		return passEnded{fmt.Errorf("no document request %s; it may have ended over 10 minutes ago", id)}
	case r.opening:
		s.mu.Unlock()
		return fmt.Errorf("the request for %s is already being answered", r.Doc)
	case r.state != StatePending:
		state := r.state
		s.mu.Unlock()
		return passEnded{fmt.Errorf("the request for %s has ended: %s", r.Doc, state)}
	case a == AnswerAlways && !r.CanAlways:
		s.mu.Unlock()
		return fmt.Errorf("%s can't be always allowed, as it didn't say which tool it is", r.Requester)
	case a == AnswerNo:
		s.endDocLocked(r, StateDeclined, "")
		s.mu.Unlock()
		return nil
	}
	r.opening = true
	s.mu.Unlock()

	url, err := s.opts.Shower.Show(ctx, r.Origin, r.Root, r.Doc, r.Requester)
	if err != nil {
		s.mu.Lock()
		r.opening = false
		s.endDocLocked(r, StateDeclined, err.Error())
		s.mu.Unlock()
		return passEnded{err}
	}

	var token string
	var allowErr error
	if a == AnswerAlways {
		token, allowErr = s.opts.DocGrants.Allow(r.Origin, r.Requester)
	}

	s.mu.Lock()
	r.opening = false
	s.openDocLocked(r, url, token)
	s.mu.Unlock()
	if allowErr != nil {
		return fmt.Errorf("%s is open, but %w", r.Doc, allowNotSaved(allowErr))
	}
	return nil
}

// endDocLocked ends a pending request, unless it has already ended.
func (s *Server) endDocLocked(r *docRecord, state State, reason string) {
	if r.state != StatePending {
		return
	}
	r.state, r.reason, r.ended = state, reason, s.opts.Now()
	close(r.done)
}

// openDocLocked records a pending request as open at url. An open record
// counts as ended when it opened, for sweeping.
func (s *Server) openDocLocked(r *docRecord, url, token string) {
	if r.state != StatePending {
		return
	}
	s.docOpens++
	r.state, r.url, r.token, r.ended, r.opened = StateOpen, url, token, s.opts.Now(), s.docOpens
	close(r.done)
}

// expireDocLocked ends a pending request whose time has passed, unless it
// is being shown.
func (s *Server) expireDocLocked(r *docRecord) {
	if r.state == StatePending && !r.opening && !s.opts.Now().Before(r.Expires) {
		r.state, r.ended = StateExpired, r.Expires
		close(r.done)
	}
}

// sweepDocsLocked expires requests and forgets those that ended long enough
// ago, except the latest open one of each tree, which GET /v1/documents
// lists while its tree is served. It keeps that one whether or not the tree
// is still served; GET /v1/documents forgets those of trees no longer
// served.
func (s *Server) sweepDocsLocked() {
	now := s.opts.Now()
	latest := s.latestDocsLocked()
	for id, r := range s.docs {
		s.expireDocLocked(r)
		if r.state != StatePending && now.Sub(r.ended) >= keep && latest[treeOf(r)] != r {
			delete(s.docs, id)
		}
	}
}

// tree is a served tree: its origin and root.
type tree struct{ origin, root string }

// treeOf is the tree r's document is shown from.
func treeOf(r *docRecord) tree { return tree{r.Origin, r.Root} }

// latestDocsLocked is the record of each tree that opened last.
func (s *Server) latestDocsLocked() map[tree]*docRecord {
	latest := map[tree]*docRecord{}
	for _, r := range s.docs {
		if r.state != StateOpen {
			continue
		}
		if l := latest[treeOf(r)]; l == nil || l.opened < r.opened {
			latest[treeOf(r)] = r
		}
	}
	return latest
}
