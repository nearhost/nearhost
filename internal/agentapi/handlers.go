package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1", s.hello)
	mux.HandleFunc("GET /v1/projects", s.projects)
	mux.HandleFunc("POST /v1/proposals", s.propose)
	mux.HandleFunc("GET /v1/proposals/{id}", s.proposal)
	mux.HandleFunc("GET /v1/doctor", s.doctor)
	mux.HandleFunc("POST /v1/projects/reload", s.reload)
	mux.HandleFunc("/", noSuchCall)
	return mux
}

// noSuchCall answers a call this server doesn't have.
func noSuchCall(w http.ResponseWriter, _ *http.Request) {
	fail(w, http.StatusNotFound, "no such call; GET /v1 says which API this is")
}

func origin(r *http.Request) string {
	o, _ := r.Context().Value(originKey{}).(string)
	return o
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	review := s.reviewer != nil
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{
		"api": Version, "origin": origin(r), "laptop": s.opts.Laptop, "interface": review,
		"capabilities": s.capabilities(),
	})
}

// capabilities lists, sorted, the calls beyond API 1's first ones that this
// server has. The API version stays 1 as calls are added, so a skill asks
// for the calls it needs here, and on an older nearhost finds them missing.
func (s *Server) capabilities() []string {
	caps := []string{}
	if s.opts.Doctor != nil {
		caps = append(caps, "doctor")
	}
	if s.opts.Reload != nil {
		caps = append(caps, "reload")
	}
	slices.Sort(caps)
	return caps
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	all := s.opts.Source.Projects(origin(r))
	if !r.URL.Query().Has("path") {
		if all == nil {
			all = []Project{}
		}
		reply(w, http.StatusOK, map[string]any{"projects": all})
		return
	}
	p, real, valid := pathQuery(w, r)
	if !valid {
		return
	}
	found, ok := holdingEither(all, p, real)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Sprintf("no project holds %s", path.Clean(p)))
		return
	}
	reply(w, http.StatusOK, found)
}

// pathQuery reads the path and real parameters, which must be absolute when
// given. ok is false when they are not, and the request has been answered.
func pathQuery(w http.ResponseWriter, r *http.Request) (p, real string, ok bool) {
	p, real = r.URL.Query().Get("path"), r.URL.Query().Get("real")
	if !path.IsAbs(p) {
		fail(w, http.StatusBadRequest, fmt.Sprintf("path must be absolute, not %q", p))
		return "", "", false
	}
	if real != "" && !path.IsAbs(real) {
		fail(w, http.StatusBadRequest, fmt.Sprintf("real must be absolute, not %q", real))
		return "", "", false
	}
	return p, real, true
}

// doctor checks the project holding the path, or the directory at it as it
// would be added when no project does. The project is null then, so an
// agent can tell a directory with nothing wrong from a failure.
func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	if s.opts.Doctor == nil {
		noSuchCall(w, r)
		return
	}
	p, real, ok := pathQuery(w, r)
	if !ok {
		return
	}
	o := origin(r)
	var project *Project
	name, dir := "", path.Clean(p)
	if found, ok := holdingEither(s.opts.Source.Projects(o), p, real); ok {
		project, name, dir = &found, found.Name, found.Path
	}
	ctx, cancel := context.WithTimeout(r.Context(), doctorTimeout)
	defer cancel()
	findings, err := s.opts.Doctor.Doctor(ctx, o, name, dir)
	if errors.Is(err, fs.ErrNotExist) {
		machine := o
		if o == config.LocalServer {
			machine = s.laptop()
		}
		fail(w, http.StatusNotFound, fmt.Sprintf("%s is not a directory on %s", dir, machine))
		return
	}
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if findings == nil {
		findings = []doctor.Finding{}
	}
	reply(w, http.StatusOK, map[string]any{"project": project, "findings": findings})
}

// holdingEither finds the project holding p, the path as the agent sees
// it, or else real, the same path with its symlinks resolved, when given.
// A project may be configured under either.
func holdingEither(projects []Project, p, real string) (Project, bool) {
	if found, ok := holding(projects, p); ok || real == "" {
		return found, ok
	}
	return holding(projects, real)
}

// holding finds the project whose path is p or the nearest parent of it.
func holding(projects []Project, p string) (Project, bool) {
	p = path.Clean(p)
	best, longest := -1, -1
	for i, pr := range projects {
		dir := path.Clean(pr.Path)
		if (p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")) && len(dir) > longest {
			best, longest = i, len(dir)
		}
	}
	if best < 0 {
		return Project{}, false
	}
	return projects[best], true
}

type proposeRequest struct {
	Path string `json:"path"`
	// Real is Path with its symlinks resolved, when the agent can tell.
	Real string `json:"real"`
	Name string `json:"name"`
	Web  int    `json:"web"`
}

// readBody decodes the JSON body of r into v. ok is false when it can't,
// and the request has been answered.
func readBody(w http.ResponseWriter, r *http.Request, v any) (ok bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusBadRequest, "the request is over 4 KiB")
			return false
		}
		fail(w, http.StatusBadRequest, "the body must be JSON like {\"path\": \"/abs/dir\"}: "+err.Error())
		return false
	}
	return true
}

func (s *Server) propose(w http.ResponseWriter, r *http.Request) {
	var req proposeRequest
	if !readBody(w, r, &req) {
		return
	}
	switch {
	case !path.IsAbs(req.Path):
		fail(w, http.StatusBadRequest, fmt.Sprintf("path must be absolute, not %q", req.Path))
		return
	case req.Real != "" && !path.IsAbs(req.Real):
		fail(w, http.StatusBadRequest, fmt.Sprintf("real must be absolute, not %q", req.Real))
		return
	case req.Web < 0 || req.Web > 65535:
		fail(w, http.StatusBadRequest, fmt.Sprintf("web must be a port between 1 and 65535, not %d", req.Web))
		return
	case !plainName(req.Name):
		fail(w, http.StatusBadRequest, "name must be at most 64 printable characters on one line")
		return
	}
	o := origin(r)
	if found, ok := holdingEither(s.opts.Source.Projects(o), req.Path, req.Real); ok {
		reply(w, http.StatusOK, map[string]any{"state": StateExists, "project": found})
		return
	}

	s.mu.Lock()
	s.sweepLocked()
	reviewer := s.reviewer
	pending := 0
	for _, p := range s.proposals {
		if p.Origin == o && p.state == StatePending && !p.reload {
			pending++
		}
	}
	if reviewer == nil {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "open nearhost on "+s.laptop()+" to review proposals; `nearhost up` cannot show them")
		return
	}
	if pending >= maxPending {
		s.mu.Unlock()
		fail(w, http.StatusTooManyRequests, fmt.Sprintf("%d proposals are already waiting for review; wait for them first", pending))
		return
	}
	p := &proposal{
		Proposal: Proposal{
			ID: newID(), Origin: o, Path: path.Clean(req.Path), Name: req.Name, Web: req.Web,
			Expires: s.opts.Now().Add(expiry),
		},
		state: StatePending,
		done:  make(chan struct{}),
	}
	s.proposals[p.ID] = p
	s.mu.Unlock()
	reviewer.Review(p.Proposal)
	reply(w, http.StatusAccepted, map[string]any{"id": p.ID, "expires_at": p.Expires.UTC().Format(time.RFC3339)})
}

type reloadRequest struct {
	Path string `json:"path"`
	// Real is Path with its symlinks resolved, when the agent can tell.
	Real string `json:"real"`
}

// reload re-reads the .nearhostrc of the project holding the path. A change
// that needs approval waits for the user in the interface, and the agent
// polls it as a proposal; one the user declined stays declined until the
// file changes, so an agent that reloads again is told, not left waiting.
func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	if s.opts.Reload == nil {
		noSuchCall(w, r)
		return
	}
	var req reloadRequest
	if !readBody(w, r, &req) {
		return
	}
	switch {
	case !path.IsAbs(req.Path):
		fail(w, http.StatusBadRequest, fmt.Sprintf("path must be absolute, not %q", req.Path))
		return
	case req.Real != "" && !path.IsAbs(req.Real):
		fail(w, http.StatusBadRequest, fmt.Sprintf("real must be absolute, not %q", req.Real))
		return
	}
	o := origin(r)
	found, ok := holdingEither(s.opts.Source.Projects(o), req.Path, req.Real)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Sprintf("no project holds %s; add it first", path.Clean(req.Path)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reloadTimeout)
	defer cancel()
	got, err := s.opts.Reload.Reload(ctx, found.Name)
	switch {
	case err != nil:
		fail(w, http.StatusBadGateway, err.Error())
		return
	case got.Problem != "":
		fail(w, http.StatusUnprocessableEntity, got.Problem)
		return
	case got.Declined:
		reply(w, http.StatusOK, map[string]any{"state": StateDeclined, "reason": declinedReason})
		return
	case len(got.Items) == 0:
		// The reload may have started or restarted the project.
		for _, p := range s.opts.Source.Projects(o) {
			if p.Name == found.Name {
				found = p
			}
		}
		reply(w, http.StatusOK, map[string]any{"state": "applied", "project": found})
		return
	}

	s.mu.Lock()
	s.sweepLocked()
	if s.reviewer == nil {
		s.mu.Unlock()
		reply(w, http.StatusConflict, map[string]any{
			"error": "open nearhost on " + s.laptop() + " to approve what .nearhostrc asks for (" +
				strings.Join(got.Items, ", ") + "); `nearhost up` cannot show it",
			"items": got.Items,
		})
		return
	}
	// Agents reload in a loop: the same change waits on one record, and a
	// newer one ends the record of the change it replaces, unless the user's
	// approval of that change is being saved (see Accepting).
	var rec *proposal
	for _, p := range s.proposals {
		if !p.reload || p.Name != found.Name || p.state != StatePending {
			continue
		}
		if p.sha == got.SHA256 && p.Origin == o {
			rec = p
		} else if s.accepting[change{p.Name, p.sha}] == 0 {
			s.endLocked(p, StateSuperseded, "", "")
		}
	}
	if rec == nil {
		rec = &proposal{
			Proposal: Proposal{
				ID: newID(), Origin: o, Path: path.Clean(found.Path), Name: found.Name,
				Expires: s.opts.Now().Add(expiry),
			},
			state:  StatePending,
			done:   make(chan struct{}),
			reload: true,
			sha:    got.SHA256,
		}
		s.proposals[rec.ID] = rec
	}
	id, expires := rec.ID, rec.Expires
	s.mu.Unlock()
	reply(w, http.StatusAccepted, map[string]any{
		"id": id, "state": StatePending, "items": got.Items, "expires_at": expires.UTC().Format(time.RFC3339),
	})
}

func (s *Server) laptop() string {
	if s.opts.Laptop == "" {
		return "your laptop"
	}
	return s.opts.Laptop
}

// plainName reports whether a proposed name could be shown in the form: at
// most 64 printable characters on one line. The form checks the rest.
func plainName(name string) bool {
	if len([]rune(name)) > 64 {
		return false
	}
	for _, c := range name {
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

func (s *Server) proposal(w http.ResponseWriter, r *http.Request) {
	wait := time.Duration(0)
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "s"))
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, fmt.Sprintf("wait must be a number of seconds, not %q", v))
			return
		}
		wait = min(time.Duration(n)*time.Second, maxWait)
	}
	o := origin(r)
	s.mu.Lock()
	s.sweepLocked()
	p := s.proposals[r.PathValue("id")]
	if p == nil || p.Origin != o {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "no such proposal; it may have ended over 10 minutes ago")
		return
	}
	s.expireLocked(p)
	s.mu.Unlock()
	s.supersedeStale(p)
	s.mu.Lock()
	done, left := p.done, p.Expires.Sub(s.opts.Now())
	s.mu.Unlock()

	if wait > 0 {
		t := time.NewTimer(min(wait, max(left, 0)))
		select {
		case <-done:
		case <-t.C:
			s.supersedeStale(p)
		case <-r.Context().Done():
			t.Stop()
			return
		}
		t.Stop()
	}

	s.mu.Lock()
	s.expireLocked(p)
	state, reason, name := p.state, p.reason, p.project
	s.mu.Unlock()
	out := map[string]any{"state": state}
	if reason != "" {
		out["reason"] = reason
	}
	if state == StateAccepted {
		project := Project{Name: name}
		for _, pr := range s.opts.Source.Projects(o) {
			if pr.Name == name {
				project = pr
			}
		}
		out["project"] = project
	}
	reply(w, http.StatusOK, out)
}
