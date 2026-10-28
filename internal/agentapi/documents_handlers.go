package agentapi

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/config"
)

// maxDocPath bounds the path and root of a document request.
const maxDocPath = 1024

// docReady answers the request as a call this server doesn't have when it
// has no documents.
func (s *Server) docReady(w http.ResponseWriter, r *http.Request) bool {
	if !s.documenting() {
		noSuchCall(w, r)
		return false
	}
	return true
}

type docReqBody struct {
	Path      string `json:"path"`
	Root      string `json:"root"`
	Requester string `json:"requester"`
	Session   string `json:"session"`
	Label     string `json:"label"`
	Token     string `json:"token"`
}

// badDocPath is why v can't be the field called name of a document
// request, or "" when it can: it must be absolute, without NUL, and at
// most maxDocPath bytes. The Shower checks the rest.
func badDocPath(name, v string) string {
	switch {
	case strings.ContainsRune(v, 0):
		return name + " must not contain NUL"
	case len(v) > maxDocPath:
		return fmt.Sprintf("%s must be at most %d bytes", name, maxDocPath)
	case !path.IsAbs(v):
		return fmt.Sprintf("%s must be absolute, not %q", name, v)
	}
	return ""
}

// machine names origin's machine for a message.
func (s *Server) machine(origin string) string {
	if origin == config.LocalServer {
		return s.laptop()
	}
	return origin
}

// notConnected is the reply for a server Nearhost isn't connected to.
func (s *Server) notConnected(w http.ResponseWriter, origin string) {
	fail(w, http.StatusConflict, fmt.Sprintf(
		"Nearhost isn't connected to %s. On %s, select %s in Nearhost and press c, then ask again", origin, s.laptop(), origin))
}

// docRequest asks to show a document: at once with a valid token or in a
// tree already served for its requester, or prompted in the interface
// otherwise.
func (s *Server) docRequest(w http.ResponseWriter, r *http.Request) {
	if !s.docReady(w, r) {
		return
	}
	var req docReqBody
	if !readBody(w, r, &req) {
		return
	}
	if msg := cmp.Or(badDocPath("path", req.Path), badDocPath("root", req.Root)); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	switch {
	case !validRequester(req.Requester):
		fail(w, http.StatusBadRequest, fmt.Sprintf("requester must be 1 to 32 lowercase letters, digits and -, such as claude-code, not %q", req.Requester))
		return
	case !validSession(req.Session):
		fail(w, http.StatusBadRequest, "session must be at most 128 printable characters")
		return
	case !plainName(req.Label):
		fail(w, http.StatusBadRequest, "label must be at most 64 printable characters on one line")
		return
	}
	o := origin(r)
	doc := path.Clean(req.Path)

	ctx, cancel := detached(r.Context(), openTimeout)
	res, err := s.opts.Shower.Resolve(ctx, o, doc, path.Clean(req.Root))
	cancel()
	switch {
	case errors.Is(err, ErrNotConnected):
		s.notConnected(w, o)
		return
	case errors.Is(err, fs.ErrNotExist):
		fail(w, http.StatusNotFound, fmt.Sprintf("%s isn't there on %s", doc, s.machine(o)))
		return
	case errors.Is(err, ErrBadDocument):
		fail(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		fail(w, http.StatusBadGateway, err.Error())
		return
	}

	allowed := false
	if req.Token != "" {
		ok, err := s.opts.DocGrants.Check(o, req.Requester, req.Token)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			fail(w, http.StatusUnauthorized, fmt.Sprintf(
				"the token for %s on %s was revoked or never existed; drop it and ask again without it", req.Requester, o))
			return
		}
		allowed = true
	}
	// A tree already served opens without asking only for the requester it
	// was approved for, and never for RequesterUnknown, which any process
	// on the machine can claim to be.
	if by, ok := s.opts.Shower.Served(o, res.Root); ok && by == req.Requester && req.Requester != RequesterUnknown {
		allowed = true
	}

	s.mu.Lock()
	s.sweepDocsLocked()
	reviewer := s.reviewer
	if !allowed {
		if rec := s.repeatLocked(o, res, req); rec != nil {
			s.mu.Unlock()
			reply(w, http.StatusAccepted, map[string]any{"id": rec.ID, "expires_at": rec.Expires.UTC().Format(time.RFC3339)})
			return
		}
		if reviewer == nil {
			s.mu.Unlock()
			fail(w, http.StatusConflict, "open nearhost on "+s.laptop()+" to approve this; `nearhost up` cannot show prompts")
			return
		}
		if n := s.docPromptsLocked(o); n >= maxPending {
			s.mu.Unlock()
			fail(w, http.StatusTooManyRequests, fmt.Sprintf("%d document requests are already waiting for approval; wait for them first", n))
			return
		}
	}
	s.docSeq++
	rec := &docRecord{
		DocRequest: DocRequest{
			ID: newID(), Origin: o, Requester: req.Requester, Session: req.Session, Label: req.Label,
			Root: res.Root, Doc: res.Doc, CanAlways: req.Requester != RequesterUnknown,
			Expires: s.opts.Now().Add(expiry),
		},
		prompted: !allowed,
		opening:  allowed,
		state:    StatePending,
		seq:      s.docSeq,
		done:     make(chan struct{}),
	}
	s.docs[rec.ID] = rec
	s.mu.Unlock()

	if !allowed {
		reviewer.ReviewDocument(rec.DocRequest)
		reply(w, http.StatusAccepted, map[string]any{"id": rec.ID, "expires_at": rec.Expires.UTC().Format(time.RFC3339)})
		return
	}

	ctx, cancel = detached(r.Context(), openTimeout)
	url, err := s.opts.Shower.Show(ctx, o, res.Root, res.Doc, req.Requester)
	cancel()
	s.mu.Lock()
	rec.opening = false
	if err != nil {
		s.endDocLocked(rec, StateDeclined, err.Error())
		s.mu.Unlock()
		switch {
		case errors.Is(err, ErrNoPage):
			fail(w, http.StatusConflict, err.Error())
		case errors.Is(err, ErrNotConnected):
			s.notConnected(w, o)
		default:
			fail(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	s.openDocLocked(rec, url, "")
	out := s.viewDocLocked(rec, false)
	reviewer = s.reviewer
	s.mu.Unlock()
	if reviewer != nil {
		reviewer.Shown(o, req.Requester, res.Doc)
	}
	reply(w, http.StatusCreated, out)
}

// repeatLocked is origin's pending prompt for the same document, requester
// and session as req, as agents may ask again while it waits, or nil.
func (s *Server) repeatLocked(origin string, res Resolved, req docReqBody) *docRecord {
	for _, r := range s.docs {
		if r.Origin == origin && r.prompted && r.state == StatePending && r.Root == res.Root && r.Doc == res.Doc &&
			r.Requester == req.Requester && r.Session == req.Session {
			return r
		}
	}
	return nil
}

// docPromptsLocked counts origin's document prompts waiting for the user.
func (s *Server) docPromptsLocked(origin string) int {
	n := 0
	for _, r := range s.docs {
		if r.Origin == origin && r.prompted && r.state == StatePending {
			n++
		}
	}
	return n
}

// viewDocLocked is a record as an agent sees it. full adds what the request
// asked for, for lists. A token is given once, and only outside lists.
func (s *Server) viewDocLocked(r *docRecord, full bool) map[string]any {
	out := map[string]any{"id": r.ID, "state": r.state}
	if r.reason != "" {
		out["reason"] = r.reason
	}
	if r.state == StateOpen {
		out["url"] = r.url
		if r.token != "" && !full {
			out["token"], r.token = r.token, ""
		}
	}
	if full {
		out["requester"], out["session"], out["label"], out["root"], out["path"] = r.Requester, r.Session, r.Label, r.Root, r.Doc
		if r.state == StatePending {
			out["expires_at"] = r.Expires.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// docGet reads one document request, long-polling while it waits for the
// user.
func (s *Server) docGet(w http.ResponseWriter, r *http.Request) {
	if !s.docReady(w, r) {
		return
	}
	wait, ok := waitQuery(w, r)
	if !ok {
		return
	}
	o := origin(r)
	s.mu.Lock()
	s.sweepDocsLocked()
	rec := s.docs[r.PathValue("id")]
	if rec == nil || rec.Origin != o {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "no such document request; it may have ended over 10 minutes ago")
		return
	}
	pending, done, left := rec.state == StatePending, rec.done, rec.Expires.Sub(s.opts.Now())
	s.mu.Unlock()

	if wait > 0 && pending {
		t := time.NewTimer(min(wait, max(left, 0)))
		select {
		case <-done:
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return
		}
		t.Stop()
	}

	s.mu.Lock()
	s.expireDocLocked(rec)
	out := s.viewDocLocked(rec, false)
	delete(out, "id")
	s.mu.Unlock()
	reply(w, http.StatusOK, out)
}

// docList lists origin's pending document requests and its served trees,
// each by the latest document opened in it.
func (s *Server) docList(w http.ResponseWriter, r *http.Request) {
	if !s.docReady(w, r) {
		return
	}
	o := origin(r)
	s.mu.Lock()
	s.sweepDocsLocked()
	var recs, open []*docRecord
	for _, rec := range s.docs {
		if rec.Origin == o && rec.state == StatePending {
			recs = append(recs, rec)
		}
	}
	for t, rec := range s.latestDocsLocked() {
		if t.origin == o {
			open = append(open, rec)
		}
	}
	s.mu.Unlock()

	// Served is asked without s.mu held. A tree the user removed is left
	// out, and its record forgotten once it would have been swept.
	var gone []*docRecord
	for _, rec := range open {
		if _, ok := s.opts.Shower.Served(o, rec.Root); ok {
			recs = append(recs, rec)
		} else {
			gone = append(gone, rec)
		}
	}

	s.mu.Lock()
	now := s.opts.Now()
	for _, rec := range gone {
		if now.Sub(rec.ended) >= keep {
			delete(s.docs, rec.ID)
		}
	}
	slices.SortFunc(recs, func(a, b *docRecord) int { return cmp.Compare(a.seq, b.seq) })
	out := []map[string]any{}
	for _, rec := range recs {
		out = append(out, s.viewDocLocked(rec, true))
	}
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"documents": out})
}
