package agentapi

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
	"unicode"

	"github.com/nearhost/nearhost/internal/config"
)

// passing reports whether this server has passthroughs.
func (s *Server) passing() bool {
	return s.opts.Passer != nil && s.opts.Grants != nil
}

// passReady answers the request as a call this server doesn't have when it
// has no passthroughs.
func (s *Server) passReady(w http.ResponseWriter, r *http.Request) bool {
	if !s.passing() {
		noSuchCall(w, r)
		return false
	}
	return true
}

type passBody struct {
	Port      int    `json:"port"`
	Requester string `json:"requester"`
	Session   string `json:"session"`
	Label     string `json:"label"`
	Token     string `json:"token"`
}

// validRequester reports whether name is lowercase letters, digits and -,
// 1 to 32 characters.
func validRequester(name string) bool {
	if len(name) < 1 || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// validSession reports whether id is at most 128 printable characters.
func validSession(id string) bool {
	if len([]rune(id)) > 128 {
		return false
	}
	for _, c := range id {
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

// localURL is the laptop URL of a forwarded port.
func localURL(port int) string {
	return fmt.Sprintf("http://localhost:%d/", port)
}

// sessionName is a session for a message.
func sessionName(id string) string {
	if id == "" {
		return "no session"
	}
	return "session " + id
}

// passRequest asks for a passthrough: open at once with a valid token, or
// prompted in the interface otherwise.
func (s *Server) passRequest(w http.ResponseWriter, r *http.Request) {
	if !s.passReady(w, r) {
		return
	}
	var req passBody
	if !readBody(w, r, &req) {
		return
	}
	o := origin(r)
	switch {
	case o == config.LocalServer:
		fail(w, http.StatusBadRequest, "a passthrough forwards a server's port to "+s.laptop()+"; this socket is the laptop's own")
		return
	case req.Port < 1 || req.Port > 65535:
		fail(w, http.StatusBadRequest, fmt.Sprintf("port must be between 1 and 65535, not %d", req.Port))
		return
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
	if s.coveredByProject(o, req.Port) {
		reply(w, http.StatusOK, map[string]any{"state": StateOpen, "via": "project", "url": localURL(req.Port)})
		return
	}

	allowed := false
	if req.Token != "" {
		ok, err := s.opts.Grants.Check(o, req.Requester, req.Token)
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
	taken := false
	if !allowed {
		taken = !s.opts.Passer.Free(req.Port)
	}
	snap := s.live(o)

	s.mu.Lock()
	s.syncLocked(snap)
	s.sweepPassesLocked()
	if s.heldLocked(w, o, req) {
		s.mu.Unlock()
		return
	}
	reviewer := s.reviewer
	if !allowed {
		if reviewer == nil {
			s.mu.Unlock()
			fail(w, http.StatusConflict, "open nearhost on "+s.laptop()+" to approve this; `nearhost up` cannot show prompts")
			return
		}
		if n := s.promptsLocked(o); n >= maxPending {
			s.mu.Unlock()
			fail(w, http.StatusTooManyRequests, fmt.Sprintf("%d passthrough requests are already waiting for approval; wait for them first", n))
			return
		}
	}
	s.passSeq++
	rec := &passRecord{
		PassRequest: PassRequest{
			ID: newID(), Origin: o, Requester: req.Requester, Session: req.Session, Label: req.Label,
			Remote: req.Port, LocalTaken: taken, CanAlways: req.Requester != RequesterUnknown,
			Expires: s.opts.Now().Add(expiry),
		},
		prompted: !allowed,
		opening:  allowed,
		state:    StatePending,
		seq:      s.passSeq,
		done:     make(chan struct{}),
	}
	s.passes[rec.ID] = rec
	p := rec.passthrough(s.opts.Now())
	s.mu.Unlock()

	if !allowed {
		reviewer.ReviewPassthrough(rec.PassRequest)
		reply(w, http.StatusAccepted, map[string]any{"id": rec.ID, "expires_at": rec.Expires.UTC().Format(time.RFC3339)})
		return
	}

	local, err := s.opts.Passer.Open(r.Context(), p)
	s.mu.Lock()
	rec.opening = false
	if err != nil {
		s.endPassLocked(rec, StateDeclined, err.Error())
		s.mu.Unlock()
		if errors.Is(err, ErrNotConnected) {
			fail(w, http.StatusConflict, fmt.Sprintf(
				"Nearhost isn't connected to %s. On %s, select %s in Nearhost and press c, then ask again", o, s.laptop(), o))
			return
		}
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if rec.state != StatePending {
		// Its session was closed while it opened.
		s.mu.Unlock()
		s.opts.Passer.Close(r.Context(), rec.ID)
		fail(w, http.StatusConflict, "its session was closed while it opened")
		return
	}
	s.openPassLocked(rec, local, "")
	out := s.viewLocked(rec, false)
	reviewer = s.reviewer
	s.mu.Unlock()
	p.Local = local
	if reviewer != nil {
		reviewer.Opened(p)
	}
	reply(w, http.StatusCreated, out)
}

// coveredByProject reports whether a running project on origin forwards
// port to the laptop already, with an applied loopback route.
func (s *Server) coveredByProject(origin string, port int) bool {
	for _, p := range s.opts.Source.Projects(origin) {
		if p.State != "running" {
			continue
		}
		for _, rt := range p.Routes {
			if rt.Role == "loopback" && rt.Port == port && rt.Applied {
				return true
			}
		}
	}
	return false
}

// heldLocked answers a request for a port that already has an open or
// pending passthrough on origin: with it, for the same requester and
// session, as agents ask again before every URL they show, or with 409 for
// anyone else. It reports whether it answered.
func (s *Server) heldLocked(w http.ResponseWriter, origin string, req passBody) bool {
	for _, r := range s.passes {
		if r.Origin != origin || r.Remote != req.Port || (r.state != StatePending && r.state != StateOpen) {
			continue
		}
		switch {
		case r.Requester != req.Requester || r.Session != req.Session:
			what := "passed through"
			if r.state == StatePending {
				what = "waiting for approval"
			}
			fail(w, http.StatusConflict, fmt.Sprintf("port %d on %s is already %s for %s (%s); ask for another port, or close that one first",
				req.Port, origin, what, r.Requester, sessionName(r.Session)))
		case r.state == StateOpen:
			reply(w, http.StatusOK, s.viewLocked(r, false))
		default:
			reply(w, http.StatusAccepted, map[string]any{"id": r.ID, "expires_at": r.Expires.UTC().Format(time.RFC3339)})
		}
		return true
	}
	return false
}

// promptsLocked counts origin's passthrough prompts waiting for the user.
func (s *Server) promptsLocked(origin string) int {
	n := 0
	for _, r := range s.passes {
		if r.Origin == origin && r.prompted && r.state == StatePending {
			n++
		}
	}
	return n
}

// viewLocked is a record as an agent sees it. full adds what the request
// asked for, for lists. A token is given once, and only outside lists.
func (s *Server) viewLocked(r *passRecord, full bool) map[string]any {
	out := map[string]any{"id": r.ID, "state": r.state}
	if r.reason != "" {
		out["reason"] = r.reason
	}
	if r.state == StateOpen {
		out["url"], out["local_port"] = localURL(r.local), r.local
		if r.token != "" && !full {
			out["token"], r.token = r.token, ""
		}
	}
	if full {
		out["port"], out["requester"], out["session"], out["label"] = r.Remote, r.Requester, r.Session, r.Label
		if r.state == StatePending {
			out["expires_at"] = r.Expires.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// passGet reads one passthrough, long-polling while it waits for the user.
func (s *Server) passGet(w http.ResponseWriter, r *http.Request) {
	if !s.passReady(w, r) {
		return
	}
	wait, ok := waitQuery(w, r)
	if !ok {
		return
	}
	o := origin(r)
	snap := s.live(o)
	s.mu.Lock()
	s.syncLocked(snap)
	s.sweepPassesLocked()
	rec := s.passes[r.PathValue("id")]
	if rec == nil || rec.Origin != o {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "no such passthrough; it may have ended over 10 minutes ago")
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
		snap = s.live(o)
	}

	s.mu.Lock()
	s.syncLocked(snap)
	s.expirePassLocked(rec)
	out := s.viewLocked(rec, false)
	delete(out, "id")
	s.mu.Unlock()
	reply(w, http.StatusOK, out)
}

// passList lists origin's open and pending passthroughs, of one session
// when ?session= is given.
func (s *Server) passList(w http.ResponseWriter, r *http.Request) {
	if !s.passReady(w, r) {
		return
	}
	o := origin(r)
	session, one := r.URL.Query().Get("session"), r.URL.Query().Has("session")
	snap := s.live(o)
	s.mu.Lock()
	s.syncLocked(snap)
	s.sweepPassesLocked()
	var recs []*passRecord
	for _, rec := range s.passes {
		if rec.Origin == o && (rec.state == StatePending || rec.state == StateOpen) && (!one || rec.Session == session) {
			recs = append(recs, rec)
		}
	}
	slices.SortFunc(recs, func(a, b *passRecord) int { return cmp.Compare(a.seq, b.seq) })
	out := []map[string]any{}
	for _, rec := range recs {
		out = append(out, s.viewLocked(rec, true))
	}
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"passthroughs": out})
}

// passDelete closes one passthrough, or ends its request.
func (s *Server) passDelete(w http.ResponseWriter, r *http.Request) {
	if !s.passReady(w, r) {
		return
	}
	o := origin(r)
	s.mu.Lock()
	s.sweepPassesLocked()
	rec := s.passes[r.PathValue("id")]
	if rec == nil || rec.Origin != o {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "no such passthrough; it may have ended over 10 minutes ago")
		return
	}
	s.endPassLocked(rec, StateClosed, "")
	open := rec.state == StateOpen
	s.mu.Unlock()
	if open {
		if err := s.closePasses(r, []*passRecord{rec}); err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// passDeleteSession closes every passthrough of a session on the origin,
// and ends its requests, as the session-end hook asks.
func (s *Server) passDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.passReady(w, r) {
		return
	}
	session := r.URL.Query().Get("session")
	if session == "" || !validSession(session) {
		fail(w, http.StatusBadRequest, "say which session: DELETE /v1/passthroughs?session=<id>, or close one with DELETE /v1/passthroughs/<id>")
		return
	}
	o := origin(r)
	s.mu.Lock()
	s.sweepPassesLocked()
	var open []*passRecord
	for _, rec := range s.passes {
		if rec.Origin != o || rec.Session != session {
			continue
		}
		s.endPassLocked(rec, StateClosed, "")
		if rec.state == StateOpen {
			open = append(open, rec)
		}
	}
	s.mu.Unlock()
	if err := s.closePasses(r, open); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// closePasses closes open passthroughs, without s.mu held, and records them
// closed. It returns the first error.
func (s *Server) closePasses(r *http.Request, recs []*passRecord) error {
	var first error
	for _, rec := range recs {
		if err := s.opts.Passer.Close(r.Context(), rec.ID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		s.mu.Lock()
		s.closePassLocked(rec)
		s.mu.Unlock()
	}
	return first
}
